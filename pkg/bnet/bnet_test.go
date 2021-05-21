package bnet

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

type getResp struct {
	data    interface{}
	headers http.Header
	err     error
}

type mockHTTP struct {
	getMap map[string]getResp
}

func (m *mockHTTP) Get(region, endpoint string) ([]byte, http.Header, error) {
	respData, ok := m.getMap[endpoint]
	if !ok {
		panic("unexpected endpoint")
	}

	b, err := json.Marshal(respData.data)
	if err != nil {
		panic(err)
	}

	return b, respData.headers, respData.err
}

type mockRealmListResp struct {
	Realms []struct {
		ID   int
		Slug string
	}
}

type mockConnectedRealmsResponse struct {
	ConnectedRealms []struct {
		Href string
	} `json:"connected_realms"`
}

type mockSingleConnRealmResp struct {
	ConnectedRealm struct {
		Href string
	} `json:"connected_realm"`
}

func TestGetRealmList(t *testing.T) {
	// realm1 and 3 are "main" realms
	// the others are part of its conn realm group, but have to be found by subsequent calls
	// the correct conn realm map should look like:
	//
	// 1 -> 2, 5
	// 3 -> 4
	realmList := mockRealmListResp{Realms: []struct {
		ID   int
		Slug string
	}{
		{
			ID:   1,
			Slug: "realm1-main",
		},
		{
			ID:   2,
			Slug: "realm2",
		},
		{
			ID:   3,
			Slug: "realm3-main",
		},
		{
			ID:   4,
			Slug: "realm4",
		},
		{
			ID:   5,
			Slug: "realm5",
		},
	}}

	connRealmList := mockConnectedRealmsResponse{ConnectedRealms: []struct{ Href string }{
		{
			Href: "https://unittest.com/data/wow/connected-realm/1?namespace=dynamic-us",
		},
		{
			Href: "https://unittest.com/data/wow/connected-realm/3?namespace=dynamic-us",
		},
	}}

	m := &mockHTTP{
		getMap: map[string]getResp{
			"data/wow/realm/index?locale=en_US&namespace=dynamic-us":           {data: realmList},
			"data/wow/connected-realm/index?locale=en_US&namespace=dynamic-us": {data: connRealmList},
			// single connected realms responses for realms that need followup queries
			"data/wow/realm/realm2?namespace=dynamic-us&locale=en_US": {data: mockSingleConnRealmResp{
				ConnectedRealm: struct{ Href string }{Href: "https://unittest.com/data/wow/connected-realm/1?namespace=dynamic-us"},
			}},
			"data/wow/realm/realm4?namespace=dynamic-us&locale=en_US": {data: mockSingleConnRealmResp{
				ConnectedRealm: struct{ Href string }{Href: "https://unittest.com/data/wow/connected-realm/3?namespace=dynamic-us"},
			}},
			"data/wow/realm/realm5?namespace=dynamic-us&locale=en_US": {data: mockSingleConnRealmResp{
				ConnectedRealm: struct{ Href string }{Href: "https://unittest.com/data/wow/connected-realm/1?namespace=dynamic-us"},
			}},
		},
	}

	realms, err := GetRealmList(m, "us")
	if err != nil {
		t.Errorf("unexpected error %s", err.Error())
		return
	}

	expected := &Realms{
		Region: "us",
		ConnectedRealms: ConnectedRealmCollection{
			"realm1-main": 1,
			"realm2":      1,
			"realm3-main": 3,
			"realm4":      3,
			"realm5":      1,
		},
		AllRealms: AllRealmCollection{
			"realm1-main": 1,
			"realm2":      2,
			"realm3-main": 3,
			"realm4":      4,
			"realm5":      5,
		},
	}

	if !reflect.DeepEqual(realms, expected) {
		t.Error("results not equal")
		return
	}
}
