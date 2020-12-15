package bnet

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/transform"

	"golang.org/x/text/unicode/norm"
)

// HTTP contains mockable bnet http calls
type HTTP interface {
	Get(region, endpoint string) ([]byte, http.Header, error)
	GetIfNotModified(region, endpoint, since string) (string, []byte, error)
	refreshOAuth() error
}

// AllRealmCollection maps a region string to a map of realm slug to connected realm id info
type AllRealmCollection map[string]int

// AllRealmCollection maps a region string to a map of realm slug to connected realm id info
type ConnectedRealmCollection map[string]int

// IsValidRealm tests the given realmSlug and returns if it is valid
func (rc AllRealmCollection) IsValidRealm(realmSlug string) bool {
	_, ok := rc[realmSlug]

	return ok
}

type Realms struct {
	Region          string
	ConnectedRealms ConnectedRealmCollection
	AllRealms       AllRealmCollection
}

// GetRealmList calculates valid realm ids for a given region
func GetRealmList(h HTTP, region string) (*Realms, error) {
	// get all realms
	resp, _, err := h.Get(region, fmt.Sprintf("data/wow/realm/index?locale=en_US&namespace=dynamic-%s",
		region))
	if err != nil {
		return nil, err
	}

	type realmListResp struct {
		Realms []struct {
			ID   int
			Slug string
		}
	}

	var rlr realmListResp
	if err := json.Unmarshal(resp, &rlr); err != nil {
		return nil, err
	}

	// get all connected realms
	resp, _, err = h.Get(region, fmt.Sprintf("data/wow/connected-realm/index?locale=en_US&namespace=dynamic-%s",
		region))
	if err != nil {
		return nil, err
	}

	type connectedRealmsResponse struct {
		ConnectedRealms []struct {
			Href string
		} `json:"connected_realms"`
	}

	var crr connectedRealmsResponse
	if err := json.Unmarshal(resp, &crr); err != nil {
		return nil, err
	}

	// connected realms are returned as urls, extract out the id
	regex := regexp.MustCompile(`connected-realm/([0-9]+)\?`)

	crLookup := make(map[int]struct{})
	for _, cr := range crr.ConnectedRealms {
		// extract id from url...
		if matches := regex.FindStringSubmatch(cr.Href); matches != nil && len(matches) > 0 {
			id, err := strconv.Atoi(matches[1])
			if err != nil {
				return nil, err
			}

			// and assign it to the map
			crLookup[id] = struct{}{}
		}
	}

	count := 0
	m := make(map[string]int)
	ar := make(map[string]int)
	for _, r := range rlr.Realms {
		ar[r.Slug] = r.ID
		// filter any realm that is not part of the connected realms list, these will be lazy loaded later
		if _, ok := crLookup[r.ID]; ok {
			m[r.Slug] = r.ID
			count++
		}
	}

	r := &Realms{
		ConnectedRealms: make(map[string]int),
		AllRealms:       make(map[string]int),
	}

	r.AllRealms = ar
	r.ConnectedRealms = m

	return r, nil
}

// ConnectedRealmID retrieves a connected realm given the region and realm slug.
func (c ConnectedRealmCollection) ConnectedRealmID(h HTTP, realmSlug, region string) (int, error) {
	if !IsValidRegion(region) {
		return -1, fmt.Errorf("invalid region")
	}

	r := strings.ToLower(strings.TrimSpace(realmSlug))

	if id, ok := c[r]; ok {
		return id, nil
	}

	// if the realm is not a top-level connected realm, it is part of a realm pool
	// we need to search for the pool it is in and save the connectedRealmID
	resp, _, err := h.Get(region, fmt.Sprintf("data/wow/search/connected-realm?namespace=dynamic-%s&locale=en_US&realms.slug=%s",
		region, r))
	if err != nil {
		return -1, err
	}

	type realmSearchResp struct {
		PageSize int // sanity check
		Results  []struct {
			Data struct {
				ID   int
				Slug string
			}
		}
	}

	var sr realmSearchResp
	if err := json.Unmarshal(resp, &sr); err != nil {
		return -1, err
	}

	i := 0
	if sr.PageSize > 1 {
		// this seems to be very uncommon
		// but, if a search result returns more than one result, it can still be valid
		// example: twisting nether US
		for j, r := range sr.Results {
			if r.Data.Slug == realmSlug {
				i = j
				break
			}
		}
	} else if sr.PageSize == 0 {
		return -1, fmt.Errorf("invalid realm slug")
	}

	id := sr.Results[i].Data.ID
	// cache the result
	c[realmSlug] = id

	return id, nil
}

// remove diacritics
func isMn(r rune) bool {
	return unicode.Is(unicode.Mn, r) // Mn: nonspacing marks
}

// RealmSlug returns the normalized realm slug representation of a realm string
func RealmSlug(realm string) string {
	rs := strings.ToLower(realm)
	rs = strings.Replace(rs, "-", "", -1)
	rs = strings.Replace(rs, "'", "", -1)
	rs = strings.Replace(rs, " ", "-", -1)

	t := transform.Chain(norm.NFD, transform.RemoveFunc(isMn), norm.NFC)
	result, _, _ := transform.String(t, rs)
	return result
}

// IsValidRegion accepts region strings "us" or "eu"
func IsValidRegion(region string) bool {
	i := strings.Index("useu", region)

	return i == 0 || i == 2
}
