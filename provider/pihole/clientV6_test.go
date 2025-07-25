/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pihole

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"sigs.k8s.io/external-dns/endpoint"
)

func TestIsValidIPv4(t *testing.T) {
	tests := []struct {
		ip       string
		expected bool
	}{
		{"192.168.1.1", true},
		{"255.255.255.255", true},
		{"0.0.0.0", true},
		{"", false},
		{"256.256.256.256", false},
		{"192.168.0.1/22", false},
		{"192.168.1", false},
		{"abc.def.ghi.jkl", false},
		{"::ffff:192.168.20.3", false},
	}

	for _, test := range tests {
		t.Run(test.ip, func(t *testing.T) {
			if got := isValidIPv4(test.ip); got != test.expected {
				t.Errorf("isValidIPv4(%s) = %v; want %v", test.ip, got, test.expected)
			}
		})
	}
}

func TestIsValidIPv6(t *testing.T) {
	tests := []struct {
		ip       string
		expected bool
	}{
		{"2001:0db8:85a3:0000:0000:8a2e:0370:7334", true},
		{"2001:db8:85a3::8a2e:370:7334", true},
		//IPv6 dual, the format is y:y:y:y:y:y:x.x.x.x.
		{"::ffff:192.168.20.3", true},
		{"::1", true},
		{"::", true},
		{"2001:db8::", true},
		{"", false},
		{":", false},
		{"::ffff:", false},
		{"192.168.20.3", false},
		{"2001:db8:85a3:0:0:8a2e:370:7334:1234", false},
		{"2001:db8:85a3::8a2e:370g:7334", false},
		{"2001:db8:85a3::8a2e:370:7334::", false},
		{"2001:db8:85a3::8a2e:370:7334::1", false},
	}

	for _, test := range tests {
		t.Run(test.ip, func(t *testing.T) {
			if got := isValidIPv6(test.ip); got != test.expected {
				t.Errorf("isValidIPv6(%s) = %v; want %v", test.ip, got, test.expected)
			}
		})
	}
}

func newTestServerV6(t *testing.T, hdlr http.HandlerFunc) *httptest.Server {
	t.Helper()
	svr := httptest.NewServer(hdlr)
	return svr
}

func TestNewPiholeClientV6(t *testing.T) {
	// Test correct error on no server provided
	_, err := newPiholeClientV6(PiholeConfig{APIVersion: "6"})
	if err == nil {
		t.Error("Expected error from config with no server")
	} else if !errors.Is(err, ErrNoPiholeServer) {
		t.Error("Expected ErrNoPiholeServer, got", err)
	}

	// Test new client with no password. Should create the client cleanly.
	cl, err := newPiholeClientV6(PiholeConfig{
		Server:     "test",
		APIVersion: "6",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cl.(*piholeClientV6); !ok {
		t.Error("Did not create a new pihole client")
	}

	// Create a test server
	srvr := newTestServerV6(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth" && r.Method == http.MethodPost {
			var requestData map[string]string
			json.NewDecoder(r.Body).Decode(&requestData)
			defer r.Body.Close()

			w.Header().Set("Content-Type", "application/json")

			if requestData["password"] != "correct" {
				// Return unsuccessful authentication response
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{
				"session": {
					"valid": false,
					"totp": false,
					"sid": null,
					"validity": -1,
					"message": "password incorrect"
				},
				"took": 0.2
			}`))
				return
			}

			// Return successful authentication response
			w.Write([]byte(`{
			"session": {
				"valid": true,
				"totp": false,
				"sid": "supersecret",
				"csrf": "csrfvalue",
				"validity": 1800,
				"message": "password correct"
			},
			"took": 0.18
		}`))
		} else {
			http.NotFound(w, r)
		}
	})
	defer srvr.Close()

	// Test invalid password
	_, err = newPiholeClientV6(
		PiholeConfig{Server: srvr.URL, APIVersion: "6", Password: "wrong"},
	)
	if err == nil {
		t.Error("Expected error for creating client with invalid password")
	}

	// Test correct password
	cl, err = newPiholeClientV6(
		PiholeConfig{Server: srvr.URL, APIVersion: "6", Password: "correct"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if cl.(*piholeClientV6).token != "supersecret" {
		t.Error("Parsed invalid token from login response:", cl.(*piholeClient).token)
	}
}

func TestListRecordsV6(t *testing.T) {
	// Create a test server
	srvr := newTestServerV6(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/config/dns/hosts" && r.Method == http.MethodGet {

			w.WriteHeader(http.StatusOK)
			w.Header().Set("Content-Type", "application/json")

			// Return A records
			w.Write([]byte(`{
				"config": {
					"dns": {
						"hosts": [
							"192.168.178.33 service1.example.com",
							"192.168.178.34 service2.example.com",
							"192.168.178.34 service3.example.com",
							"192.168.178.35 service8.example.com",
							"192.168.178.36 service8.example.com",
							"fc00::1:192:168:1:1 service4.example.com",
							"fc00::1:192:168:1:2 service5.example.com",
							"fc00::1:192:168:1:3 service6.example.com",
							"::ffff:192.168.20.3 service7.example.com",
							"fc00::1:192:168:1:4 service9.example.com",
							"fc00::1:192:168:1:5 service9.example.com",
							"192.168.20.3 service7.example.com"
						]
					}
				},
				"took": 5
			}`))
		} else if r.URL.Path == "/api/config/dns/cnameRecords" && r.Method == http.MethodGet {

			w.WriteHeader(http.StatusOK)
			w.Header().Set("Content-Type", "application/json")

			// Return A records
			w.Write([]byte(`{
				"config": {
					"dns": {
						"cnameRecords": [
							"source1.example.com,target1.domain.com,1000",
							"source2.example.com,target2.domain.com,50",
							"source3.example.com,target3.domain.com"
						]
					}
				},
				"took": 5
			}`))
		} else {
			http.NotFound(w, r)
		}
	})
	defer srvr.Close()

	// Create a client
	cfg := PiholeConfig{
		Server:     srvr.URL,
		APIVersion: "6",
	}
	cl, err := newPiholeClientV6(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Ensure A records were parsed correctly
	expected := []*endpoint.Endpoint{
		{
			DNSName: "service1.example.com",
			Targets: []string{"192.168.178.33"},
		},
		{
			DNSName: "service2.example.com",
			Targets: []string{"192.168.178.34"},
		},
		{
			DNSName: "service3.example.com",
			Targets: []string{"192.168.178.34"},
		},
		{
			DNSName: "service7.example.com",
			Targets: []string{"192.168.20.3"},
		},
		{
			DNSName: "service8.example.com",
			Targets: []string{"192.168.178.35", "192.168.178.36"},
		},
	}
	// Test retrieve A records unfiltered
	arecs, err := cl.listRecords(context.Background(), endpoint.RecordTypeA)
	if err != nil {
		t.Fatal(err)
	}

	expectedMap := make(map[string]*endpoint.Endpoint)
	for _, ep := range expected {
		expectedMap[ep.DNSName] = ep
	}
	for _, rec := range arecs {
		if ep, ok := expectedMap[rec.DNSName]; ok {
			if cmp.Diff(ep.Targets, rec.Targets) != "" {
				t.Errorf("Got invalid targets for %s: %v, expected: %v", rec.DNSName, rec.Targets, ep.Targets)
			}
		}
	}

	// Ensure AAAA records were parsed correctly
	expected = []*endpoint.Endpoint{
		{
			DNSName: "service4.example.com",
			Targets: []string{"fc00::1:192:168:1:1"},
		},
		{
			DNSName: "service5.example.com",
			Targets: []string{"fc00::1:192:168:1:2"},
		},
		{
			DNSName: "service6.example.com",
			Targets: []string{"fc00::1:192:168:1:3"},
		},
		{
			DNSName: "service7.example.com",
			Targets: []string{"::ffff:192.168.20.3"},
		},
		{
			DNSName: "service9.example.com",
			Targets: []string{"fc00::1:192:168:1:4", "fc00::1:192:168:1:5"},
		},
	}

	// Test retrieve AAAA records unfiltered
	arecs, err = cl.listRecords(context.Background(), endpoint.RecordTypeAAAA)
	if err != nil {
		t.Fatal(err)
	}

	if len(arecs) != len(expected) {
		t.Fatalf("Expected %d AAAA records returned, got: %d", len(expected), len(arecs))
	}

	expectedMap = make(map[string]*endpoint.Endpoint)
	for _, ep := range expected {
		expectedMap[ep.DNSName] = ep
	}
	for _, rec := range arecs {
		if ep, ok := expectedMap[rec.DNSName]; ok {
			if cmp.Diff(ep.Targets, rec.Targets) != "" {
				t.Errorf("Got invalid targets for %s: %v, expected: %v", rec.DNSName, rec.Targets, ep.Targets)
			}
		}
	}

	// Ensure CNAME records were parsed correctly
	expected = []*endpoint.Endpoint{
		{
			DNSName:   "source1.example.com",
			Targets:   []string{"target1.domain.com"},
			RecordTTL: 1000,
		},
		{
			DNSName:   "source2.example.com",
			Targets:   []string{"target2.domain.com"},
			RecordTTL: 50,
		},
		{
			DNSName: "source3.example.com",
			Targets: []string{"target3.domain.com"},
		},
	}

	// Test retrieve CNAME records unfiltered
	cnamerecs, err := cl.listRecords(context.Background(), endpoint.RecordTypeCNAME)
	if err != nil {
		t.Fatal(err)
	}
	if len(cnamerecs) != len(expected) {
		t.Fatalf("Expected %d CAME records returned, got: %d", len(expected), len(cnamerecs))
	}

	expectedMap = make(map[string]*endpoint.Endpoint)
	for _, ep := range expected {
		expectedMap[ep.DNSName] = ep
	}
	for _, rec := range arecs {
		if ep, ok := expectedMap[rec.DNSName]; ok {
			if cmp.Diff(ep.Targets, rec.Targets) != "" {
				t.Errorf("Got invalid targets for %s: %v, expected: %v", rec.DNSName, rec.Targets, ep.Targets)
			}
		}
	}

	// Note: filtered tests are not needed since A/AAAA records are tested filtered already
	// and cnameRecords have their own element

	// unsupported type
	_, err = cl.listRecords(context.Background(), endpoint.RecordTypeNAPTR)
	if err == nil || err.Error() != fmt.Sprintf("unsupported record type: %s", endpoint.RecordTypeNAPTR) {
		t.Fatal("Expected error for using unsupported record type")
	}
}

func TestErrorsV6(t *testing.T) {
	//Error test cases

	// Create a client
	cfgErrURL := PiholeConfig{
		Server:     "not an url",
		APIVersion: "6",
	}
	clErrURL, _ := newPiholeClientV6(cfgErrURL)

	_, err := clErrURL.listRecords(context.Background(), endpoint.RecordTypeCNAME)
	if err == nil {
		t.Fatal("Expected error for using invalid URL")
	}
	_, err = clErrURL.listRecords(nil, endpoint.RecordTypeCNAME)
	if err == nil {
		t.Fatal("Expected error for nil context")
	}
	// Unmarshalling error
	srvrErrJson := newTestServerV6(t, func(w http.ResponseWriter, r *http.Request) {

		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/json")

		// Return A records
		w.Write([]byte(`I am not JSON`))
	})
	defer srvrErrJson.Close()
	// Create a client
	cfgErr := PiholeConfig{
		Server:     srvrErrJson.URL,
		APIVersion: "6",
	}
	clErr, _ := newPiholeClientV6(cfgErr)

	resp, err := clErr.listRecords(context.Background(), endpoint.RecordTypeA)
	if err == nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(err.Error(), "failed to unmarshal error response:") {
		t.Fatal("Expected unmarshalling error, got:", err)
	}

	// bad record format return by server
	srvrErr := newTestServerV6(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/config/dns/hosts" && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			w.Header().Set("Content-Type", "application/json")

			// Return A records
			w.Write([]byte(`{
				"config": {
					"dns": {
						"hosts": [
							"192.168.178.33"
						]
					}
				},
				"took": 5
			}`))
		} else if r.URL.Path == "/api/config/dns/cnameRecords" && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			w.Header().Set("Content-Type", "application/json")

			// Return A records
			w.Write([]byte(`{
				"config": {
					"dns": {
						"cnameRecords": [
							"source1.example.com,target1.domain.com,100",
							"source2.example.com,target2.domain.com,not_an_integer"
						]
					}
				},
				"took": 5
			}`))
		} else {
			http.NotFound(w, r)
		}
	})
	defer srvrErr.Close()

	// Create a client
	cfgErr = PiholeConfig{
		Server:     srvrErr.URL,
		APIVersion: "6",
	}
	clErr, _ = newPiholeClientV6(cfgErr)

	resp, err = clErr.listRecords(context.Background(), endpoint.RecordTypeA)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp) != 0 {
		t.Fatal("Expected no records returned, got:", len(resp))
	}
	resp, err = clErr.listRecords(context.Background(), endpoint.RecordTypeCNAME)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp) != 2 {
		t.Fatal("Expected one records returned, got:", len(resp))
	}

	expected := []*endpoint.Endpoint{
		{
			DNSName:   "source1.example.com",
			Targets:   []string{"target1.domain.com"},
			RecordTTL: 100,
		},
		{
			DNSName: "source2.example.com",
			Targets: []string{"target2.domain.com"},
		},
	}

	expectedMap := make(map[string]*endpoint.Endpoint)
	for _, ep := range expected {
		expectedMap[ep.DNSName] = ep
	}
	for _, rec := range resp {
		if ep, ok := expectedMap[rec.DNSName]; ok {
			if cmp.Diff(ep.Targets, rec.Targets) != "" {
				t.Errorf("Got invalid targets for %s: %v, expected: %v", rec.DNSName, rec.Targets, ep.Targets)
			}
			if ep.RecordTTL != rec.RecordTTL {
				t.Errorf("Got invalid TTL for %s: %d, expected: %d", rec.DNSName, rec.RecordTTL, ep.RecordTTL)
			}
		} else {
			t.Errorf("Unexpected record found: %s", rec.DNSName)
		}
	}

}

func TestTokenValidity(t *testing.T) {
	srvok := newTestServerV6(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth" && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			w.Header().Set("Content-Type", "application/json")

			// Return bad content
			w.Write([]byte(`{
			"session": {
				"valid": true,
				"totp": false,
				"sid": "supersecret",
				"csrf": "csrfvalue",
				"validity": 1800,
				"message": "password correct"
			},
			"took": 0.17
			}`))
		}
	})
	// Create a client
	cfgOK := PiholeConfig{
		Server:     srvok.URL,
		APIVersion: "6",
	}
	clOK, err := newPiholeClientV6(cfgOK)
	clOK.(*piholeClientV6).token = "valid"
	validity, err := clOK.(*piholeClientV6).checkTokenValidity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !validity {
		t.Fatal("Should be valid")
	}

	// Create a test server
	srvr := newTestServerV6(t, func(w http.ResponseWriter, r *http.Request) {

		if r.URL.Path == "/api/auth" && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			w.Header().Set("Content-Type", "application/json")

			// Return bad content
			w.Write([]byte(`Not a JSON`))
		}
	})
	defer srvr.Close()
	//
	// Create a client
	cfg := PiholeConfig{
		Server:     srvr.URL,
		APIVersion: "6",
	}
	cl, err := newPiholeClientV6(cfg)
	if err != nil {
		t.Fatal(err)
	}
	validity, err = cl.(*piholeClientV6).checkTokenValidity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if validity {
		t.Fatal("Should be invalid : no token")
	}
	// Test token validity
	cl.(*piholeClientV6).token = "valid"

	validity, err = cl.(*piholeClientV6).checkTokenValidity(nil)
	if err != nil {
		t.Fatal(err)
	}
	if validity {
		t.Fatal("Should be invalid : nil context")
	}

	validity, err = cl.(*piholeClientV6).checkTokenValidity(context.Background())
	if err == nil {
		t.Fatal("Should be invalid : failed to unmarshal error")
	}
	if !strings.HasPrefix(err.Error(), "failed to unmarshal error response") {
		t.Fatal("Expected unmarshalling error, got:", err)
	}
	if validity {
		t.Fatal("Should be invalid : unmarshalling error")
	}
}

func TestDo(t *testing.T) {

	srvDo := newTestServerV6(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/ok" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// Return bad content
			w.Write([]byte(`{
			"session": {
				"valid": true,
				"totp": false,
				"sid": "supersecret",
				"csrf": "csrfvalue",
				"validity": 1800,
				"message": "password correct"
			},
			"took": 0.16
			}`))
		} else if r.URL.Path == "/api/auth" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// Return bad content
			w.Write([]byte(`{
			"session": {
				"valid": false,
				"totp": false,
				"sid": "",
				"csrf": "csrfvalue",
				"validity": 1800,
				"message": "password correct"
			},
			"took": 0.15
			}`))
		} else if r.URL.Path == "/api/auth" && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusUnauthorized)
			// Return bad content
			w.Write([]byte(`{
			"error": {
				"key": "401",
				"message": "Expired token",
				"hint": "Expired token"
			},
			"took": 0.14
			}`))
		} else if r.URL.Path == "/api/auth/418" && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusTeapot)
			// Return bad content
			w.Write([]byte(`{
			"error": {
				"key": "418",
				"message": "I'm a teapot",
				"hint": "It is a teapot"
			},
			"took": 0.13
			}`))
		} else if r.URL.Path == "/api/auth/nojson" && r.Method == http.MethodGet {
			// Return bad content
			w.WriteHeader(http.StatusTeapot)
			w.Write([]byte(`Not a JSON`))
		} else if r.URL.Path == "/api/auth/401" && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusUnauthorized)
			// Return bad content
			w.Write([]byte(`{
			"error": {
				"key": "401",
				"message": "Expired token",
				"hint": "Expired token"
			},
			"took": 0.10
			}`))
		}
	})
	defer srvDo.Close()

	// Create a client
	cfg := PiholeConfig{
		Server:     srvDo.URL,
		APIVersion: "6",
	}
	cl, err := newPiholeClientV6(cfg)
	cl.(*piholeClientV6).token = "valid"

	rq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srvDo.URL+"/api/auth/ok", nil)
	resp, err := cl.(*piholeClientV6).do(rq)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp) == 0 {
		t.Fatal("Should have a response")
	}
	// Test not handled error code
	rq, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, srvDo.URL+"/api/auth/418", nil)
	resp, err = cl.(*piholeClientV6).do(rq)
	if resp != nil {
		t.Fatal(err)
	}
	if err == nil {
		t.Fatal("Should have an error")
	}
	if !strings.HasPrefix(err.Error(), "received 418 status code from request") {
		t.Fatal("Expected error for unexpected status code, got:", err)
	}
	// Test error on non JSON response
	rq, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, srvDo.URL+"/api/auth/nojson", nil)
	resp, err = cl.(*piholeClientV6).do(rq)
	if resp != nil {
		t.Fatal(err)
	}
	if err == nil {
		t.Fatal("Should have an error")
	}
	if !strings.HasPrefix(err.Error(), "failed to unmarshal error response") {
		t.Fatal("Expected error for unmarshal", err)
	}
	// Test Unauthorized retry failed
	rq, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, srvDo.URL+"/api/auth/401", nil)
	resp, err = cl.(*piholeClientV6).do(rq)
	if resp != nil {
		t.Fatal(err)
	}
	if err == nil {
		t.Fatal("Should have an error")
	}
	if !strings.HasPrefix(err.Error(), "max tries reached for token renewal") {
		t.Fatal("Expected error for max tries reached", err)
	}
}

func TestDoRetryOne(t *testing.T) {
	nbCall := 0
	srvRetry := newTestServerV6(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// Return bad content
			w.Write([]byte(`{
			"session": {
				"valid": true,
				"totp": false,
				"sid": "123465468",
				"csrf": "csrfvalue",
				"validity": 1800,
				"message": "password correct"
			},
			"took": 0.24
			}`))
		} else if r.URL.Path == "/api/auth/401" && r.Method == http.MethodGet {
			if nbCall == 0 {
				w.WriteHeader(http.StatusUnauthorized)
				// Return bad content
				w.Write([]byte(`{
				"error": {
					"key": "401",
					"message": "Expired token",
					"hint": "Expired token"
				},
				"took": 0.25
				}`))
			} else {
				w.WriteHeader(http.StatusOK)
				// Return bad content
				w.Write([]byte(`Success`))
			}
			nbCall += 1
		}
	})
	defer srvRetry.Close()
	// Create a client
	cfgRetryOK := PiholeConfig{
		Server:     srvRetry.URL,
		APIVersion: "6",
	}
	clRetryOK, err := newPiholeClientV6(cfgRetryOK)
	clRetryOK.(*piholeClientV6).token = "valid"
	// Test Unauthorized refresh OK
	rq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srvRetry.URL+"/api/auth/401", nil)
	resp, err := clRetryOK.(*piholeClientV6).do(rq)
	if err != nil {
		t.Fatal("Should succeed", err)
	}
	if string(resp) != "Success" {
		t.Fatal("Should have a response")
	}

}

func TestCreateRecordV6(t *testing.T) {
	var ep *endpoint.Endpoint
	srvr := newTestServerV6(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && (r.URL.Path == "/api/config/dns/hosts/192.168.1.1 test.example.com" ||
			r.URL.Path == "/api/config/dns/hosts/fc00::1:192:168:1:1 test.example.com" ||
			r.URL.Path == "/api/config/dns/cnameRecords/source1.example.com,target1.domain.com" ||
			r.URL.Path == "/api/config/dns/hosts/192.168.1.2 test.example.com" ||
			r.URL.Path == "/api/config/dns/hosts/192.168.1.3 test.example.com" ||
			r.URL.Path == "/api/config/dns/hosts/fc00::1:192:168:1:2 test.example.com" ||
			r.URL.Path == "/api/config/dns/hosts/fc00::1:192:168:1:3 test.example.com" ||
			r.URL.Path == "/api/config/dns/cnameRecords/source2.example.com,target2.domain.com,500") {

			// Return A records
			w.WriteHeader(http.StatusCreated)
		} else {
			http.NotFound(w, r)
		}
	})
	defer srvr.Close()

	// Create a client
	cfg := PiholeConfig{
		Server:       srvr.URL,
		APIVersion:   "6",
		DomainFilter: endpoint.NewDomainFilter([]string{"example.com"}),
	}
	cl, err := newPiholeClientV6(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Test create A record
	ep = &endpoint.Endpoint{
		DNSName:    "test.example.com",
		Targets:    []string{"192.168.1.1"},
		RecordType: endpoint.RecordTypeA,
	}
	if err := cl.createRecord(context.Background(), ep); err != nil {
		t.Fatal(err)
	}

	// Test create multiple A records
	ep = &endpoint.Endpoint{
		DNSName:    "test.example.com",
		Targets:    []string{"192.168.1.2", "192.168.1.3"},
		RecordType: endpoint.RecordTypeA,
	}
	if err := cl.createRecord(context.Background(), ep); err != nil {
		t.Fatal(err)
	}

	// Test create AAAA record
	ep = &endpoint.Endpoint{
		DNSName:    "test.example.com",
		Targets:    []string{"fc00::1:192:168:1:1"},
		RecordType: endpoint.RecordTypeAAAA,
	}
	if err := cl.createRecord(context.Background(), ep); err != nil {
		t.Fatal(err)
	}

	// Test create multiple AAAA records
	ep = &endpoint.Endpoint{
		DNSName:    "test.example.com",
		Targets:    []string{"fc00::1:192:168:1:2", "fc00::1:192:168:1:3"},
		RecordType: endpoint.RecordTypeAAAA,
	}
	if err := cl.createRecord(context.Background(), ep); err != nil {
		t.Fatal(err)
	}

	// Test create CNAME record
	ep = &endpoint.Endpoint{
		DNSName:    "source1.example.com",
		Targets:    []string{"target1.domain.com"},
		RecordType: endpoint.RecordTypeCNAME,
	}
	if err := cl.createRecord(context.Background(), ep); err != nil {
		t.Fatal(err)
	}

	// Test create CNAME record with TTL
	ep = &endpoint.Endpoint{
		DNSName:    "source2.example.com",
		Targets:    []string{"target2.domain.com"},
		RecordTTL:  endpoint.TTL(500),
		RecordType: endpoint.RecordTypeCNAME,
	}
	if err := cl.createRecord(context.Background(), ep); err != nil {
		t.Fatal(err)
	}

	// Test create CNAME record with multiple targets and ensure it fails
	ep = &endpoint.Endpoint{
		DNSName:    "source3.example.com",
		Targets:    []string{"target3.domain.com", "target4.domain.com"},
		RecordType: endpoint.RecordTypeCNAME,
	}
	if err := cl.createRecord(context.Background(), ep); err == nil {
		t.Fatal(err)
	}

	// Test create a wildcard record and ensure it fails
	ep = &endpoint.Endpoint{
		DNSName:    "*.example.com",
		Targets:    []string{"192.168.1.1"},
		RecordType: endpoint.RecordTypeA,
	}
	if err := cl.createRecord(context.Background(), ep); err == nil {
		t.Fatal(err)
	}

	// Skip not matching domain
	ep = &endpoint.Endpoint{
		DNSName:    "foo.bar.com",
		Targets:    []string{"192.168.1.1"},
		RecordType: endpoint.RecordTypeA,
	}
	err = cl.createRecord(context.Background(), ep)
	if err != nil {
		t.Fatal("Should not return error on non filtered domain")
	}

	// Not supported type
	ep = &endpoint.Endpoint{
		DNSName:    "test.example.com",
		Targets:    []string{"192.168.1.1"},
		RecordType: "not a type",
	}
	err = cl.createRecord(context.Background(), ep)
	if err != nil {
		t.Fatal("Should not return error on unsupported type")
	}

	// Create a client
	cfgDr := PiholeConfig{
		Server:       srvr.URL,
		APIVersion:   "6",
		DomainFilter: endpoint.NewDomainFilter([]string{"example.com"}),
		DryRun:       true,
	}
	clDr, err := newPiholeClientV6(cfgDr)
	if err != nil {
		t.Fatal(err)
	}
	// Skip Dry Run
	ep = &endpoint.Endpoint{
		DNSName:    "test.example.com",
		Targets:    []string{"192.168.1.1"},
		RecordType: endpoint.RecordTypeA,
	}
	err = clDr.createRecord(context.Background(), ep)
	if err != nil {
		t.Fatal("Should not return error on dry run")
	}
	// skip missing targets
	ep = &endpoint.Endpoint{
		DNSName:    "test.example.com",
		Targets:    []string{},
		RecordType: endpoint.RecordTypeA,
	}
	err = clDr.createRecord(context.Background(), ep)
	if err != nil {
		t.Fatal("Should not return error on missing targets")
	}
}

func TestDeleteRecordV6(t *testing.T) {
	var ep *endpoint.Endpoint
	srvr := newTestServerV6(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && (r.URL.Path == "/api/config/dns/hosts/192.168.1.1 test.example.com" ||
			r.URL.Path == "/api/config/dns/hosts/fc00::1:192:168:1:1 test.example.com" ||
			r.URL.Path == "/api/config/dns/cnameRecords/source1.example.com,target1.domain.com" ||
			r.URL.Path == "/api/config/dns/cnameRecords/source2.example.com,target2.domain.com,500") {

			// Return A records
			w.WriteHeader(http.StatusNoContent)
		} else {
			http.NotFound(w, r)
		}
	})
	defer srvr.Close()

	// Create a client
	cfg := PiholeConfig{
		Server:     srvr.URL,
		APIVersion: "6",
	}
	cl, err := newPiholeClientV6(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Test delete A record
	ep = &endpoint.Endpoint{
		DNSName:    "test.example.com",
		Targets:    []string{"192.168.1.1"},
		RecordType: endpoint.RecordTypeA,
	}
	if err := cl.deleteRecord(context.Background(), ep); err != nil {
		t.Fatal(err)
	}

	// Test delete AAAA record
	ep = &endpoint.Endpoint{
		DNSName:    "test.example.com",
		Targets:    []string{"fc00::1:192:168:1:1"},
		RecordType: endpoint.RecordTypeAAAA,
	}
	if err := cl.deleteRecord(context.Background(), ep); err != nil {
		t.Fatal(err)
	}

	// Test delete CNAME record
	ep = &endpoint.Endpoint{
		DNSName:    "source1.example.com",
		Targets:    []string{"target1.domain.com"},
		RecordType: endpoint.RecordTypeCNAME,
	}
	if err := cl.deleteRecord(context.Background(), ep); err != nil {
		t.Fatal(err)
	}

	// Test delete CNAME record with TTL
	ep = &endpoint.Endpoint{
		DNSName:    "source2.example.com",
		Targets:    []string{"target2.domain.com"},
		RecordTTL:  endpoint.TTL(500),
		RecordType: endpoint.RecordTypeCNAME,
	}
	if err := cl.deleteRecord(context.Background(), ep); err != nil {
		t.Fatal(err)
	}
}
