/*
Copyright 2015 The Kubernetes Authors All rights reserved.

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

package scheduler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"k8s.io/kubernetes/pkg/api"
	client "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/plugin/pkg/scheduler/algorithm"
	schedulerapi "k8s.io/kubernetes/plugin/pkg/scheduler/api"
)

const (
	DefaultExtenderTimeout = 5 * time.Second
)

// HTTPExtender implements the algorithm.SchedulerExtender interface.
type HTTPExtender struct {
	extenderURL    string
	filterVerb     string
	prioritizeVerb string
	weight         int
	apiVersion     string
	client         *http.Client
}

func makeTransport(config *schedulerapi.ExtenderConfig) (http.RoundTripper, error) {
	var cfg client.Config
	if config.TLSConfig != nil {
		cfg.TLSClientConfig = *config.TLSConfig
	}
	if config.EnableHttps {
		hasCA := len(cfg.CAFile) > 0 || len(cfg.CAData) > 0
		if !hasCA {
			cfg.Insecure = true
		}
	}
	tlsConfig, err := client.TLSConfigFor(&cfg)
	if err != nil {
		return nil, err
	}
	if tlsConfig != nil {
		return &http.Transport{
			TLSClientConfig: tlsConfig,
		}, nil
	}
	return http.DefaultTransport, nil
}

func NewHTTPExtender(config *schedulerapi.ExtenderConfig, apiVersion string) (algorithm.SchedulerExtender, error) {
	if config.HTTPTimeout.Nanoseconds() == 0 {
		config.HTTPTimeout = time.Duration(DefaultExtenderTimeout)
	}

	transport, err := makeTransport(config)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   config.HTTPTimeout,
	}
	return &HTTPExtender{
		extenderURL:    config.URLPrefix,
		apiVersion:     apiVersion,
		filterVerb:     config.FilterVerb,
		prioritizeVerb: config.PrioritizeVerb,
		weight:         config.Weight,
		client:         client,
	}, nil
}

// Filter based on extender implemented predicate functions. The filtered list is
// expected to be a subset of the supplied list.
func (h *HTTPExtender) Filter(pod *api.Pod, nodes *api.NodeList) (*api.NodeList, error) {
	var result schedulerapi.ExtenderFilterResult

	if h.filterVerb == "" {
		return nodes, nil
	}

	args := schedulerapi.ExtenderArgs{
		Pod:   *pod,
		Nodes: *nodes,
	}

	if err := h.send(h.filterVerb, &args, &result); err != nil {
		return nil, err
	}
	if result.Error != "" {
		return nil, fmt.Errorf(result.Error)
	}
	return &result.Nodes, nil
}

// Prioritize based on extender implemented priority functions. Weight*priority is added
// up for each such priority function. The returned score is added to the score computed
// by Kubernetes scheduler. The total score is used to do the host selection.
func (h *HTTPExtender) Prioritize(pod *api.Pod, nodes *api.NodeList) (*schedulerapi.HostPriorityList, int, error) {
	var result schedulerapi.HostPriorityList

	if h.prioritizeVerb == "" {
		result := schedulerapi.HostPriorityList{}
		for _, node := range nodes.Items {
			result = append(result, schedulerapi.HostPriority{Host: node.Name, Score: 0})
		}
		return &result, 0, nil
	}

	args := schedulerapi.ExtenderArgs{
		Pod:   *pod,
		Nodes: *nodes,
	}

	if err := h.send(h.prioritizeVerb, &args, &result); err != nil {
		return nil, 0, err
	}
	return &result, h.weight, nil
}

// Helper function to send messages to the extender
func (h *HTTPExtender) send(action string, args interface{}, result interface{}) error {
	out, err := json.Marshal(args)
	if err != nil {
		return err
	}

	url := h.extenderURL + "/" + h.apiVersion + "/" + action

	req, err := http.NewRequest("POST", url, bytes.NewReader(out))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(body, result); err != nil {
		return err
	}
	return nil
}
