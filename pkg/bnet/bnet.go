package bnet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sync/errgroup"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/transform"

	"golang.org/x/text/unicode/norm"
)

// HTTP contains mockable bnet http calls
type HTTP interface {
	Get(region, endpoint string) ([]byte, http.Header, error)
}

// AllRealmCollection maps a realm slug to a blizzard realm ID
type AllRealmCollection map[string]int

// ConnectedRealmCollection maps connected realm ID -> realm slug.
type ConnectedRealmCollection map[int][]string

// IsValidRealm tests the given realmSlug and returns if it is valid
func (rc AllRealmCollection) IsValidRealm(realmSlug string) bool {
	_, ok := rc[realmSlug]

	return ok
}

type Realms struct {
	Region          string
	ConnectedRealms ConnectedRealmCollection
	AllRealms       AllRealmCollection
	crRealm         map[string]int
}

// GetRealmList calculates valid realm ids for a given region
// This methods makes many API calls to populate the full list of connected realms
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

	crc := ConnectedRealmCollection(make(map[int][]string))
	ar := AllRealmCollection(make(map[string]int))
	crRealm := make(map[string]int)
	rs := realmScanner{
		lock: &sync.RWMutex{},
	}

	crCh := make(chan int, len(crr.ConnectedRealms))
	crcCheck := make(map[string]int)
	for _, cr := range crr.ConnectedRealms {
		// extract id from url...
		if matches := regex.FindStringSubmatch(cr.Href); matches != nil && len(matches) > 0 {
			id, err := strconv.Atoi(matches[1])
			if err != nil {
				return nil, err
			}

			crCh <- id
		}
	}
	close(crCh)

	ctx := context.Background()
	eg, ctx := errgroup.WithContext(ctx)
	threads := 5
	for i := 0; i < threads; i++ {
		eg.Go(func() error {
			for crID := range crCh {
				c := crID
				// scrape all realms attached to this connected realm, mutate crc
				realms, err := rs.scrapeConnRealm(h, region, c, crc)
				if err != nil {
					return err
				}

				rs.lock.Lock()
				for _, r := range realms {
					crcCheck[r]++
					crRealm[r] = c
				}
				rs.lock.Unlock()

				return nil
			}

			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	for _, r := range rlr.Realms {
		ar[r.Slug] = r.ID
		crcCheck[r.Slug]--
		if crcCheck[r.Slug] == 0 {
			delete(crcCheck, r.Slug)
		}
	}

	if len(crcCheck) > 0 {
		return nil, errors.New("realms not completely scraped")
	}

	r := &Realms{
		ConnectedRealms: crc,
		AllRealms:       ar,
		Region:          region,
		crRealm:         crRealm,
	}

	return r, nil
}

// realmScanner contains a lock which allows scrapeConnRealm to be called in parallel
type realmScanner struct {
	lock *sync.RWMutex
}

var r realmScanner

func init() {
	r = realmScanner{
		lock: &sync.RWMutex{},
	}
}

// ConnectedRealmID retrieves a connected realm given the region and realm slug.
// Note maps are always passed by reference, so a pointer receiver here doesn't matter!
func (r Realms) ConnectedRealmID(h HTTP, realmSlug string) (int, error) {
	id, ok := r.crRealm[realmSlug]
	if !ok {
		return -1, errors.New("realm not found in region")
	}

	return id, nil
}

func (r *realmScanner) scrapeConnRealm(h HTTP, region string, connRealmId int, c ConnectedRealmCollection) ([]string, error) {
	resp, _, err := h.Get(region, fmt.Sprintf("/data/wow/connected-realm/%d?namespace=dynamic-%s&locale=en_US",
		connRealmId, region))
	if err != nil {
		return nil, err
	}

	type crResp struct {
		Realms []struct {
			ID   int
			Slug string
		}
	}

	var crr crResp
	if err := json.Unmarshal(resp, &crr); err != nil {
		return nil, err
	}

	realms := make([]string, len(crr.Realms))
	// only lock once :shrug:
	r.lock.Lock()
	for i, realmResp := range crr.Realms {
		id := realmResp.ID
		slug := realmResp.Slug
		if id == 0 || slug == "" {
			r.lock.Unlock()
			return nil, errors.New("blank realm response")
		}

		realms[i] = slug
		if _, ok := c[connRealmId]; ok {
			c[connRealmId] = append(c[connRealmId], slug)
		} else {
			c[connRealmId] = []string{slug}
		}
	}

	r.lock.Unlock()

	return realms, nil
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
