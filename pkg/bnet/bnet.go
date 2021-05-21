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

	m := ConnectedRealmCollection(make(map[string]int))
	ar := AllRealmCollection(make(map[string]int))
	rs := &realmScanner{}

	ctx := context.Background()
	eg, ctx := errgroup.WithContext(ctx)
	ch := make(chan string, len(rlr.Realms))
	for _, r := range rlr.Realms {
		ar[r.Slug] = r.ID
		if _, ok := crLookup[r.ID]; ok {
			m[r.Slug] = r.ID
		} else {
			// retrieve these realms in parallel later
			ch <- r.Slug
		}
	}

	for slug := range ch {
		eg.Go(func() error {
			// this realm is non-root, do a secondary query to find its connRealmID
			// connRealmID has a side effect of updating m
			// and is thread safe thanks to the lock in rs
			_, err := rs.connRealmID(h, slug, region, m)
			if err != nil {
				return err
			}

			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	r := &Realms{
		ConnectedRealms: m,
		AllRealms:       ar,
		Region:          region,
	}

	return r, nil
}

// realmScanner contains a lock which allows connRealmID to be called in parallel
type realmScanner struct {
	lock *sync.RWMutex
}

var r *realmScanner

// ConnectedRealmID retrieves a connected realm given the region and realm slug.
// Note maps are always passed by reference, so a pointer receiver here doesn't matter!
func (c ConnectedRealmCollection) ConnectedRealmID(h HTTP, realmSlug, region string) (int, error) {
	return r.connRealmID(h, realmSlug, region, c)
}

func (r *realmScanner) connRealmID(h HTTP, realmSlug, region string, c ConnectedRealmCollection) (int, error) {
	if !IsValidRegion(region) {
		return -1, fmt.Errorf("invalid region")
	}

	sanitized := strings.ToLower(strings.TrimSpace(realmSlug))

	r.lock.RLock()
	if id, ok := c[sanitized]; ok {
		return id, nil
	}
	r.lock.RUnlock()

	// connected realms are returned as urls, extract out the id
	regex := regexp.MustCompile(`connected-realm/([0-9]+)\?`)

	// if the realm is not a top-level connected realm, it is part of a realm pool
	// we need to search for the pool it is in and save the connectedRealmID
	resp, _, err := h.Get(region, fmt.Sprintf("data/wow/realm/%s?namespace=dynamic-%s&locale=en_US",
		sanitized, region))
	if err != nil {
		return -1, err
	}

	type realmResp struct {
		ConnectedRealm struct {
			Href string
		} `json:"connected_realm"`
	}

	var rr realmResp
	if err := json.Unmarshal(resp, &rr); err != nil {
		return -1, err
	}

	if matches := regex.FindStringSubmatch(rr.ConnectedRealm.Href); matches != nil && len(matches) > 0 {
		id, err := strconv.Atoi(matches[1])
		if err != nil {
			return -1, err
		}

		r.lock.Lock()
		// cache the result
		c[realmSlug] = id
		r.lock.Unlock()

		return id, nil
	}

	return -1, errors.New("could not find connected realm in href for realm")
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
