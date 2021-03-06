/*
Copyright 2016 The Kubernetes Authors All rights reserved.

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

package test_release_1_1

import (
	testgroup_unversioned "k8s.io/kubernetes/cmd/libs/go2idl/client-gen/testoutput/testgroup/unversioned"
	unversioned "k8s.io/kubernetes/pkg/client/unversioned"
)

type Interface interface {
	Testgroup() testgroup_unversioned.TestgroupClient
}

// Clientset contains the clients for groups. Each group has exactly one
// version included in a Clientset.
type Clientset struct {
	*testgroup_unversioned.TestgroupClient
}

// Testgroup retrieves the TestgroupClient
func (c *Clientset) Testgroup() *testgroup_unversioned.TestgroupClient {
	return c.TestgroupClient
}

// NewForConfig creates a new Clientset for the given config.
func NewForConfig(c *unversioned.Config) (*Clientset, error) {
	var clientset Clientset
	var err error
	clientset.TestgroupClient, err = testgroup_unversioned.NewForConfig(c)
	if err != nil {
		return nil, err
	}

	return &clientset, nil
}

// NewForConfigOrDie creates a new Clientset for the given config and
// panics if there is an error in the config.
func NewForConfigOrDie(c *unversioned.Config) *Clientset {
	var clientset Clientset
	clientset.TestgroupClient = testgroup_unversioned.NewForConfigOrDie(c)

	return &clientset
}

// New creates a new Clientset for the given RESTClient.
func New(c *unversioned.RESTClient) *Clientset {
	var clientset Clientset
	clientset.TestgroupClient = testgroup_unversioned.New(c)

	return &clientset
}
