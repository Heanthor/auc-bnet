package bnet

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
)

// OAuthResponse is the response struct for a client_credentials request
type oAuthResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

type BNet struct {
	httpClient *http.Client

	oAuthUrl string
	apiUrl   string

	currentToken oAuthResponse

	clientID     string
	clientSecret string

	log *zerolog.Logger
}

type Options struct {
	// Default: on
	EnableLogging     bool
	ProductionLogging bool
	LogLevel          string
}

const regionPlaceholder = "{region}"

var ErrNoAccessToken = errors.New("could not retrieve access token")

func New(clientID, clientSecret, oAuthUrl, apiUrl string, options *Options) (*BNet, error) {
	if err := validateBaseUrl(oAuthUrl); err != nil {
		return nil, fmt.Errorf("validate oauthUrl: %+v", err)
	}

	if err := validateBaseUrl(apiUrl); err != nil {
		return nil, fmt.Errorf("validate apiUrl: %+v", err)
	}

	var logger zerolog.Logger
	if !options.EnableLogging {
		logger = zerolog.Nop()
	} else if !options.ProductionLogging {
		logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	} else {
		logger = zerolog.Logger{}
	}

	logger = logger.With().Str("in", "auc-bnet").Logger()

	b := BNet{
		httpClient:   &http.Client{},
		clientID:     clientID,
		clientSecret: clientSecret,
		oAuthUrl:     oAuthUrl,
		apiUrl:       apiUrl,
		log:          &logger,
	}

	if err := b.refreshOAuth(); err != nil {
		return nil, err
	}

	return &b, nil
}

// validateBaseUrl checks if a baseUrl has the expected region placeholder.
// This is need because battle.net urls expect a region, usually as the subdomain, but
// for local testing, it might not be possible to place it in the same place
func validateBaseUrl(baseUrl string) error {
	if strings.Contains(baseUrl, regionPlaceholder) {
		return nil
	} else {
		return fmt.Errorf("%s does not contain region placeholder \"%s\"", baseUrl, regionPlaceholder)
	}
}

func subRegion(base, path, region string) string {
	// in case an endpoint is not prefixed with '/'
	endpoint := strings.Trim(path, " ")
	sep := ""
	if endpoint[0] != '/' {
		sep = "/"
	}

	return strings.Replace(fmt.Sprintf("%s%s%s", base, sep, endpoint), regionPlaceholder, region, -1)
}

func (b *BNet) refreshOAuth() error {
	req, err := http.NewRequest("GET",
		subRegion(b.oAuthUrl, "/oauth/token?grant_type=client_credentials", "us"),
		nil)
	if err != nil {
		b.log.Err(err).Msg("Error creating bnet Request")

		return err
	}

	req.SetBasicAuth(b.clientID, b.clientSecret)

	// begin error hell
	resp, err := b.httpClient.Do(req)
	if err != nil {
		b.log.Err(err).Msg("Error creating bnet Request")

		return err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		b.log.Err(err).Msg("Error reading response body")

		return err
	}

	var respStruct oAuthResponse

	if err = json.Unmarshal(body, &respStruct); err != nil {
		b.log.Err(err).Str("body", string(body)).Msg("Error unmarshalling response")

		return err
	}

	if len(respStruct.AccessToken) == 0 {
		b.log.Error().Msg("Could not retrieve access token")

		return ErrNoAccessToken
	}

	b.log.Info().Msg("Authenticated with Battle.net API")

	b.currentToken = respStruct

	return nil
}

// Get wraps http.Get.
// Get also handles OAuth credentials and retries.
func (b *BNet) Get(region, endpoint string) ([]byte, http.Header, error) {
	url := subRegion(b.apiUrl, endpoint, region)
	status, headers, body, err := b.get(url)

	// retry once if a random 500 happens, sometimes it will resolve itself
	if status == http.StatusInternalServerError {
		status, _, body, err = b.get(url)
	}

	if status > 0 && status != 200 {
		b.log.Error().
			Str("url", url).
			Int("statusCode", status).
			Str("body", string(body)).
			Msg("BNet.Get failed")

		return nil, nil, fmt.Errorf("response code %d", status)
	}

	return body, headers, err
}

func (b *BNet) get(url string, headers ...[]string) (int, http.Header, []byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		b.log.Error().Msg("Could not retrieve access token")

		return -1, nil, nil, err
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", b.currentToken.AccessToken))

	for _, h := range headers {
		if h != nil {
			req.Header.Add(h[0], h[1])
		}
	}

	response, err := b.httpClient.Do(req)
	if err != nil {
		b.log.Err(err).Msg("Error in http Do GET")
		return -1, nil, nil, err
	}

	defer response.Body.Close()

	b.log.Debug().
		Str("url", url).
		Int("status", response.StatusCode).
		Msg("Bnet API Request")

	if response.StatusCode == 401 {
		// OAuth is invalid, refresh
		log.Info().Msg("Token expired, refreshing")
		if err := b.refreshOAuth(); err != nil {
			return -1, nil, nil, err
		}

		return b.get(url)
	}

	rawContents, err := ioutil.ReadAll(response.Body)
	if err != nil {
		b.log.Err(err).Msg("Error in http reading response body")
		return -1, nil, nil, err
	}

	return response.StatusCode, response.Header, rawContents, nil
}

// GetIfNotModified sets the If-Modified-Since header and returns true if a response was received, false otherwise
// If a response is returned, return the value of the Last-Modified header
func (b *BNet) GetIfNotModified(region, endpoint string, since string) (string, []byte, error) {
	var h []string
	if since != "" {
		h = []string{"If-Modified-Since", since}
	}

	url := subRegion(b.apiUrl, endpoint, region)

	status, headers, body, err := b.get(url, h)
	if err != nil || status == http.StatusNotModified {
		return "", nil, err
	}

	if status != http.StatusOK {
		return "", body, fmt.Errorf("GetIfNotModified returned %d: %s", status, string(body))
	}

	return headers.Get("Last-Modified"), body, err
}
