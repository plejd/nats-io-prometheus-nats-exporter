// Copyright 2017-2023 The NATS Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package collector

import (
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	pet "github.com/nats-io/prometheus-nats-exporter/test"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// Enable/disable debug logging in tests
var Debug bool

// return fqName from parsing the Desc() field of a metric.
func parseDesc(desc string) string {
	// split on quotes.
	return strings.Split(desc, "\"")[1]
}

func verifyCollector(system, url string, endpoint string, cases map[string]float64, t *testing.T) {
	// create a new collector.
	servers := make([]*CollectedServer, 1)
	servers[0] = &CollectedServer{
		ID:  "id",
		URL: url,
	}
	coll := NewCollector(system, endpoint, "", servers)

	// now collect the metrics
	c := make(chan prometheus.Metric)
	go coll.Collect(c)
	for {
		select {
		case metric := <-c:
			pb := &dto.Metric{}
			if err := metric.Write(pb); err != nil {
				t.Fatalf("Unable to write metric: %v", err)
			}
			gauge := pb.GetGauge()
			val := gauge.GetValue()

			name := parseDesc(metric.Desc().String())
			expected, ok := cases[name]
			if ok {
				if val != expected {
					t.Fatalf("Expected %s=%v, got %v", name, expected, val)
				}
			}
		case <-time.After(10 * time.Millisecond):
			return
		}
	}
}

// To account for the metrics that share the same descriptor but differ in their variable label values,
// return a list of lists of label pairs for each of the supplied metric names.
func getLabelValues(system, url, endpoint string, metricNames []string) (map[string][]map[string]string, error) {
	labelValues := make(map[string][]map[string]string)
	namesMap := make(map[string]bool)
	for _, metricName := range metricNames {
		namesMap[metricName] = true
	}

	metrics := make(chan prometheus.Metric)
	done := make(chan bool)
	errs := make(chan error)

	// kick off the processing goroutine
	go func() {
		for {
			metric, more := <-metrics
			if more {
				metricName := parseDesc(metric.Desc().String())
				if _, ok := namesMap[metricName]; ok {
					pb := &dto.Metric{}
					if err := metric.Write(pb); err != nil {
						errs <- err
						return
					}

					labelMaps := labelValues[metricName]

					// build a map[string]string out of the []*dto.LabelPair
					labelMap := make(map[string]string)
					for _, labelPair := range pb.GetLabel() {
						labelMap[labelPair.GetName()] = labelPair.GetValue()
					}

					labelMaps = append(labelMaps, labelMap)
					labelValues[metricName] = labelMaps
				}
			} else {
				done <- true
				return
			}
		}
	}()

	// create a new collector and collect
	servers := make([]*CollectedServer, 1)
	servers[0] = &CollectedServer{
		ID:  "id",
		URL: url,
	}
	coll := NewCollector(system, endpoint, "", servers)
	coll.Collect(metrics)
	close(metrics)

	// return after the processing goroutine is done
	select {
	case err := <-errs:
		return nil, err
	case <-done:
		return labelValues, nil
	}
}

func TestServerIDFromVarz(t *testing.T) {
	s := pet.RunServer()
	defer s.Shutdown()

	url := fmt.Sprintf("http://localhost:%d/", pet.MonitorPort)
	result := GetServerIDFromVarz(url, 2*time.Second)
	if len(result) < 1 || result[0] != 'N' {
		t.Fatalf("Unexpected server id: %v", result)
	}
}

func TestServerNameFromVarz(t *testing.T) {
	serverName := "nats-server"
	s := pet.RunServerWithName(serverName)
	defer s.Shutdown()

	url := fmt.Sprintf("http://localhost:%d/", pet.MonitorPort)
	result := GetServerNameFromVarz(url, 2*time.Second)
	if result != serverName {
		t.Fatalf("Unexpected server name: %v", result)
	}
}

func TestVarz(t *testing.T) {
	s := pet.RunServer()
	defer s.Shutdown()

	url := fmt.Sprintf("http://localhost:%d/", pet.MonitorPort)

	nc := pet.CreateClientConnSubscribeAndPublish(t)
	defer nc.Close()

	// see if we get the same stats as the original monitor testing code.
	// just for our monitoring_port

	cases := map[string]float64{
		"gnatsd_varz_total_connections": 2,
		"gnatsd_varz_connections":       1,
		"gnatsd_varz_in_msgs":           1,
		"gnatsd_varz_out_msgs":          1,
		"gnatsd_varz_in_bytes":          5,
		"gnatsd_varz_out_bytes":         5,
		"gnatsd_varz_subscriptions":     61,
	}

	verifyCollector(CoreSystem, url, "varz", cases, t)
}

func TestStartAndConfigLoadTimeVarz(t *testing.T) {
	s := pet.RunServer()
	defer s.Shutdown()

	varz, err := s.Varz(nil)
	if err != nil {
		t.Fatal(err)
	}

	url := fmt.Sprintf("http://localhost:%d/", pet.MonitorPort)

	nc := pet.CreateClientConnSubscribeAndPublish(t)
	defer nc.Close()

	// see if we get the same stats as the original monitor testing code.
	// just for our monitoring_port

	cases := map[string]float64{
		"gnatsd_varz_start":            float64(varz.Start.UnixMilli()),
		"gnatsd_varz_config_load_time": float64(varz.ConfigLoadTime.UnixMilli()),
	}

	verifyCollector(CoreSystem, url, "varz", cases, t)
}

func TestConnz(t *testing.T) {
	s := pet.RunServer()
	defer s.Shutdown()

	url := fmt.Sprintf("http://localhost:%d", pet.MonitorPort)
	// see if we get the same stats as the original monitor testing code.
	// just for our monitoring_port

	cases := map[string]float64{
		"gnatsd_connz_total_connections": 0,
		"gnatsd_connz_pending_bytes":     0,
		"gnatsd_varz_connections":        0,
	}

	verifyCollector(CoreSystem, url, "connz", cases, t)

	// Test with connections.

	cases = map[string]float64{
		"gnatsd_connz_total_connections": 1,
		"gnatsd_connz_pending_bytes":     0,
		"gnatsd_varz_connections":        1,
	}
	nc := pet.CreateClientConnSubscribeAndPublish(t)
	defer nc.Close()

	verifyCollector(CoreSystem, url, "connz", cases, t)
}

func TestHealthz(t *testing.T) {
	s := pet.RunServer()
	defer s.Shutdown()

	url := fmt.Sprintf("http://localhost:%d", pet.MonitorPort)
	// see if we get the same stats as the original monitor testing code.
	// just for our monitoring_port

	cases := map[string]float64{
		"gnatsd_healthz_status":       0,
		"gnatsd_healthz_status_value": 1,
	}

	verifyCollector(CoreSystem, url, "healthz", cases, t)

	// test after server shutdown
	s.Shutdown()

	cases = map[string]float64{
		"gnatsd_healthz_status_value": 0,
	}

	verifyCollector(CoreSystem, url, "healthz", cases, t)
}

func TestNoServer(t *testing.T) {
	url := fmt.Sprintf("http://localhost:%d", pet.MonitorPort)

	cases := map[string]float64{
		"gnatsd_connz_total_connections": 0,
		"gnatsd_varz_connections":        0,
	}

	verifyCollector(CoreSystem, url, "varz", cases, t)
}

func TestRegister(t *testing.T) {
	cs := &CollectedServer{
		ID:  "myid",
		URL: fmt.Sprintf("http://localhost:%d", pet.MonitorPort),
	}
	servers := make([]*CollectedServer, 0)
	servers = append(servers, cs)

	// check duplicates do not panic
	servers = append(servers, cs)

	NewCollector("test", "varz", "", servers)

	// test idenpotency.
	nc := NewCollector("test", "varz", "", servers)

	// test without a server (no error).
	if err := prometheus.Register(nc); err != nil {
		t.Fatal("Failed to register collector:", err)
	}
	if len(nc.(*NATSCollector).Stats) > 0 {
		t.Fatal("Did not expect to get collector stats.")
	}
	prometheus.Unregister(nc)

	// start a server
	s := pet.RunServer()
	defer s.Shutdown()

	// test collect with a server
	nc = NewCollector("test", "varz", "", servers)
	if err := prometheus.Register(nc); err != nil {
		t.Fatal("Failed to register collector:", err)
	}
	if len(nc.(*NATSCollector).Stats) == 0 {
		t.Fatalf("Expected to get collector stats.")
	}
	prometheus.Unregister(nc)

	// test collect with an invalid endpoint
	nc = NewCollector("test", "GARBAGE", "", servers)
	if err := prometheus.Register(nc); err != nil {
		t.Fatal("Failed to register collector:", err)
	}
	if len(nc.(*NATSCollector).Stats) > 0 {
		t.Fatal("Did not expect to get collector stats.")
	}
	prometheus.Unregister(nc)
}

func TestAllEndpoints(t *testing.T) {
	s := pet.RunServer()
	defer s.Shutdown()

	nc := pet.CreateClientConnSubscribeAndPublish(t)
	defer nc.Close()

	url := fmt.Sprintf("http://localhost:%d", pet.MonitorPort)
	// see if we get the same stats as the original monitor testing code.
	// just for our monitoring_port

	cases := map[string]float64{
		"gnatsd_varz_connections": 1,
	}
	verifyCollector(CoreSystem, url, "varz", cases, t)

	cases = map[string]float64{
		"gnatsd_routez_num_routes": 0,
	}
	verifyCollector(CoreSystem, url, "routez", cases, t)

	cases = map[string]float64{
		"gnatsd_subsz_num_subscriptions": 61,
	}
	verifyCollector(CoreSystem, url, "subsz", cases, t)

	cases = map[string]float64{
		"gnatsd_connz_total_connections": 1,
	}
	verifyCollector(CoreSystem, url, "connz", cases, t)

	cases = map[string]float64{
		"gnatsd_healthz_status": 0,
	}
	verifyCollector(CoreSystem, url, "healthz", cases, t)
}

func TestLeafzMetricLabels(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	s := pet.RunLeafzStaticServer(&wg)
	defer s.Close()

	url := fmt.Sprintf("http://localhost:%d", pet.StaticPort)

	outmsgs := "gnatsd_leafz_conn_out_msgs"
	labelValues, err := getLabelValues(CoreSystem, url, "leafz", []string{outmsgs})
	if err != nil {
		t.Fatalf("Unexpected error getting labels for %s metrics: %v", outmsgs, err)
	}
	labelMaps, found := labelValues[outmsgs]
	if !found || len(labelMaps) != 2 {
		t.Fatalf("No info found for metric %s", outmsgs)
	}
	expectedLabelMaps := []map[string]string{
		{
			"name":      "leafz_server",
			"account":   "$G",
			"ip":        "127.0.0.1",
			"port":      "6223",
			"server_id": "id",
		},
		{
			"name":      "",
			"account":   "$G",
			"ip":        "127.0.0.2",
			"port":      "6224",
			"server_id": "id",
		},
	}
	expectedLabelsNotFound := make(map[string]string, 0)
	for _, expLabelMap := range expectedLabelMaps {
		for expLabel, expValue := range expLabelMap {
			flag := false
			for _, labelMap := range labelMaps {
				if value, ok := labelMap[expLabel]; ok && value == expValue {
					flag = true
					break
				}
			}
			if !flag {
				expectedLabelsNotFound[expLabel] = expValue
			}
		}
	}
	if len(expectedLabelsNotFound) > 0 {
		t.Fatalf("the following expected labels were missing: %v", expectedLabelsNotFound)
	}
}

func TestJetStreamMetrics(t *testing.T) {
	clientPort := 4229
	monitorPort := 8229
	s, err := pet.RunJetStreamServerWithPorts(clientPort, monitorPort, "ABC")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		os.RemoveAll(s.StoreDir())
		s.Shutdown()
	}()

	url := fmt.Sprintf("http://127.0.0.1:%d/", monitorPort)
	nc, err := nats.Connect(fmt.Sprintf("nats://localhost:%d", clientPort))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}

	_, err = js.AddStream(&nats.StreamConfig{
		Name: "foo",
	})
	if err != nil {
		t.Fatal(err)
	}

	sub, err := js.SubscribeSync("foo", nats.Durable("my-name"))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Unsubscribe()

	js.Publish("foo", []byte("bar1"))
	js.Publish("foo", []byte("bar2"))
	js.Publish("foo", []byte("bar3"))
	time.Sleep(5 * time.Second)

	cases := map[string]float64{
		"jetstream_server_total_streams":   1,
		"jetstream_server_total_consumers": 1,
	}
	verifyCollector(JetStreamSystem, url, "jsz", cases, t)
}

func TestMapKeys(t *testing.T) {
	m := map[string]any{
		"foo": "bar",
		"baz": "quux",
		"nested": map[string]any{
			"foo": "bar",
			"baz": "quux",
			"nested": map[string]any{
				"foo": "bar",
				"baz": "quux",
			},
		},
	}
	expected := map[string]struct{}{
		"foo":               {},
		"baz":               {},
		"nested_foo":        {},
		"nested_baz":        {},
		"nested_nested_foo": {},
		"nested_nested_baz": {},
	}
	keys := mapKeys(m, "")
	if !maps.Equal(keys, expected) {
		t.Fatalf("expected %v, got %v", expected, keys)
	}
}

func TestJetStreamAccountMetrics(t *testing.T) {
	// Enable debug logging
	Debug = true

	// Create a test server to serve mock response
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("Test server received request for: %s\n", r.URL.Path)
		switch r.URL.Path {
		case "/varz":
			w.Header().Set("Content-Type", "application/json")
			response := `{"server_id":"SERVER_ID","name":"nats-server"}`
			fmt.Fprintln(w, response)
			fmt.Printf("Sending varz response: %s\n", response)
		case "/jsz":
			w.Header().Set("Content-Type", "application/json")
			response := pet.JszAccountsTestResponse()
			fmt.Fprintln(w, response)
			fmt.Printf("Sending jsz response with length: %d\n", len(response))
		default:
			fmt.Printf("Not found: %s\n", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	// Create a new collector
	servers := make([]*CollectedServer, 1)
	servers[0] = &CollectedServer{
		ID:  "SERVER_ID",
		URL: ts.URL,
	}
	fmt.Printf("Server URL: %s\n", ts.URL)
	coll := NewCollector(JetStreamSystem, "accounts", "", servers)
	fmt.Printf("Collector created with system=%s, endpoint=%s\n", JetStreamSystem, "accounts")

	// Collect the metrics
	c := make(chan prometheus.Metric)
	foundMetrics := make(map[string]bool)
	expectedMetrics := []string{
		"jetstream_account_max_memory",
		"jetstream_account_max_storage",
		"jetstream_account_memory_used",
		"jetstream_account_storage_used",
	}

	// Expected values for account1
	account1Expected := map[string]float64{
		"jetstream_account_max_memory":   1073741824,  // 1 GB
		"jetstream_account_max_storage":  10737418240, // 10 GB
		"jetstream_account_memory_used":  234567890,   // ~223 MB
		"jetstream_account_storage_used": 3456789012,  // ~3.2 GB
	}

	// Expected values for account2
	account2Expected := map[string]float64{
		"jetstream_account_max_memory":   536870912,  // 512 MB
		"jetstream_account_max_storage":  5368709120, // 5 GB
		"jetstream_account_memory_used":  123456789,  // ~117 MB
		"jetstream_account_storage_used": 1356789012, // ~1.3 GB
	}

	go coll.Collect(c)
	for {
		select {
		case metric := <-c:
			pb := &dto.Metric{}
			if err := metric.Write(pb); err != nil {
				t.Fatalf("Unable to write metric: %v", err)
			}

			name := parseDesc(metric.Desc().String())
			if Debug {
				fmt.Printf("Received metric: %s with value %v\n", name, pb.GetGauge().GetValue())

				// Print labels
				labels := pb.GetLabel()
				for _, label := range labels {
					if label.GetName() == "account" {
						fmt.Printf("  Account label: %s\n", label.GetValue())
					}
				}
			}

			// Check if this is one of the account metrics we're interested in
			for _, metricName := range expectedMetrics {
				if name == metricName {
					// Get the labels to identify which account this is for
					labels := pb.GetLabel()
					var accountName string
					for _, label := range labels {
						if label.GetName() == "account" {
							accountName = label.GetValue()
							break
						}
					}

					// Verify metrics for the different accounts
					var expected float64
					switch accountName {
					case "account1":
						expected = account1Expected[name]
						if pb.GetGauge().GetValue() != expected {
							t.Fatalf("For account %s, expected %s=%v, got %v",
								accountName, name, expected, pb.GetGauge().GetValue())
						}
						foundMetrics[name+"_account1"] = true
					case "account2":
						expected = account2Expected[name]
						if pb.GetGauge().GetValue() != expected {
							t.Fatalf("For account %s, expected %s=%v, got %v",
								accountName, name, expected, pb.GetGauge().GetValue())
						}
						foundMetrics[name+"_account2"] = true
					}
				}
			}

		case <-time.After(1 * time.Second):
			// Verify that we found all expected metrics for both accounts
			if len(foundMetrics) != len(expectedMetrics)*2 {
				t.Fatalf("Did not find all expected metrics. Found: %v", foundMetrics)
			}
			return
		}
	}
}
