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

package podtask

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/pborman/uuid"
	"k8s.io/kubernetes/contrib/mesos/pkg/offers"
	annotation "k8s.io/kubernetes/contrib/mesos/pkg/scheduler/meta"
	"k8s.io/kubernetes/contrib/mesos/pkg/scheduler/metrics"
	"k8s.io/kubernetes/pkg/api"

	log "github.com/golang/glog"
	mesos "github.com/mesos/mesos-go/mesosproto"
	mutil "github.com/mesos/mesos-go/mesosutil"
)

type StateType int

const (
	StatePending StateType = iota
	StateRunning
	StateFinished
	StateUnknown
)

type FlagType string

const (
	Launched = FlagType("launched")
	Bound    = FlagType("bound")
	Deleted  = FlagType("deleted")
)

var starRole = []string{"*"}

// A struct that describes a pod task.
type T struct {
	ID  string
	Pod api.Pod

	// Stores the final procurement result, once set read-only.
	// Meant to be set by algorith.SchedulerAlgorithm only.
	Spec *Spec

	Offer       offers.Perishable // thread-safe
	State       StateType
	Flags       map[FlagType]struct{}
	CreateTime  time.Time
	UpdatedTime time.Time // time of the most recent StatusUpdate we've seen from the mesos master

	podStatus       api.PodStatus
	prototype       *mesos.ExecutorInfo // readonly
	frameworkRoles  []string            // Mesos framework roles, pods are allowed to be launched with those
	defaultPodRoles []string            // roles under which pods are scheduled if none are specified in labels
	podKey          string
	launchTime      time.Time
	bindTime        time.Time
	mapper          HostPortMapper
}

type Port struct {
	Port uint64
	Role string
}

type Spec struct {
	SlaveID       string
	AssignedSlave string
	Resources     []*mesos.Resource
	PortMap       []HostPortMapping
	Data          []byte
	Executor      *mesos.ExecutorInfo
}

// mostly-clone this pod task. the clone will actually share the some fields:
//   - executor    // OK because it's read only
//   - Offer       // OK because it's guarantees safe concurrent access
func (t *T) Clone() *T {
	if t == nil {
		return nil
	}

	// shallow-copy
	clone := *t

	// deep copy
	clone.Flags = map[FlagType]struct{}{}
	for k := range t.Flags {
		clone.Flags[k] = struct{}{}
	}
	return &clone
}

func (t *T) HasAcceptedOffer() bool {
	return t.Spec != nil
}

func (t *T) GetOfferId() string {
	if t.Offer == nil {
		return ""
	}
	return t.Offer.Details().Id.GetValue()
}

func generateTaskName(pod *api.Pod) string {
	ns := pod.Namespace
	if ns == "" {
		ns = api.NamespaceDefault
	}
	return fmt.Sprintf("%s.%s.pods", pod.Name, ns)
}

func (t *T) BuildTaskInfo() (*mesos.TaskInfo, error) {
	if t.Spec == nil {
		return nil, errors.New("no podtask.T.Spec given, cannot build task info")
	}

	info := &mesos.TaskInfo{
		Name:      proto.String(generateTaskName(&t.Pod)),
		TaskId:    mutil.NewTaskID(t.ID),
		Executor:  t.Spec.Executor,
		Data:      t.Spec.Data,
		Resources: t.Spec.Resources,
		SlaveId:   mutil.NewSlaveID(t.Spec.SlaveID),
	}

	return info, nil
}

// Clear offer-related details from the task, should be called if/when an offer
// has already been assigned to a task but for some reason is no longer valid.
func (t *T) Reset() {
	log.V(3).Infof("Clearing offer(s) from pod %v", t.Pod.Name)
	t.Offer = nil
	t.Spec = nil
}

func (t *T) Set(f FlagType) {
	t.Flags[f] = struct{}{}
	if Launched == f {
		t.launchTime = time.Now()
		queueWaitTime := t.launchTime.Sub(t.CreateTime)
		metrics.QueueWaitTime.Observe(metrics.InMicroseconds(queueWaitTime))
	}
}

func (t *T) Has(f FlagType) (exists bool) {
	_, exists = t.Flags[f]
	return
}

// Roles returns the valid roles under which this pod task can be scheduled.
// If the pod has roles annotations defined they are being used
// else default pod roles are being returned.
func (t *T) Roles() (result []string) {
	if r, ok := t.Pod.ObjectMeta.Annotations[annotation.RolesKey]; ok {
		roles := strings.Split(r, ",")

		for i, r := range roles {
			roles[i] = strings.TrimSpace(r)
		}

		return filterRoles(
			roles,
			not(emptyRole), not(seenRole()), inRoles(t.frameworkRoles...),
		)
	}

	// no roles label defined, return defaults
	return t.defaultPodRoles
}

func New(ctx api.Context, id string, pod *api.Pod, prototype *mesos.ExecutorInfo, frameworkRoles, defaultPodRoles []string) (*T, error) {
	if prototype == nil {
		return nil, fmt.Errorf("illegal argument: executor is nil")
	}

	if len(frameworkRoles) == 0 {
		frameworkRoles = starRole
	}

	if len(defaultPodRoles) == 0 {
		defaultPodRoles = starRole
	}

	key, err := MakePodKey(ctx, pod.Name)
	if err != nil {
		return nil, err
	}

	if id == "" {
		id = "pod." + uuid.NewUUID().String()
	}

	task := &T{
		ID:              id,
		Pod:             *pod,
		State:           StatePending,
		podKey:          key,
		mapper:          NewHostPortMapper(pod),
		Flags:           make(map[FlagType]struct{}),
		prototype:       prototype,
		frameworkRoles:  frameworkRoles,
		defaultPodRoles: defaultPodRoles,
	}
	task.CreateTime = time.Now()

	return task, nil
}

func (t *T) SaveRecoveryInfo(dict map[string]string) {
	dict[annotation.TaskIdKey] = t.ID
	dict[annotation.SlaveIdKey] = t.Spec.SlaveID
	dict[annotation.OfferIdKey] = t.Offer.Details().Id.GetValue()
	dict[annotation.ExecutorIdKey] = t.Spec.Executor.ExecutorId.GetValue()
}

// reconstruct a task from metadata stashed in a pod entry. there are limited pod states that
// support reconstruction. if we expect to be able to reconstruct state but encounter errors
// in the process then those errors are returned. if the pod is in a seemingly valid state but
// otherwise does not support task reconstruction return false. if we're able to reconstruct
// state then return a reconstructed task and true.
//
// at this time task reconstruction is only supported for pods that have been annotated with
// binding metadata, which implies that they've previously been associated with a task and
// that mesos knows about it.
//
// assumes that the pod data comes from the k8s registry and reflects the desired state.
//
func RecoverFrom(pod api.Pod) (*T, bool, error) {
	// we only expect annotations if pod has been bound, which implies that it has already
	// been scheduled and launched
	if pod.Spec.NodeName == "" && len(pod.Annotations) == 0 {
		log.V(1).Infof("skipping recovery for unbound pod %v/%v", pod.Namespace, pod.Name)
		return nil, false, nil
	}

	// only process pods that are not in a terminal state
	switch pod.Status.Phase {
	case api.PodPending, api.PodRunning, api.PodUnknown: // continue
	default:
		log.V(1).Infof("skipping recovery for terminal pod %v/%v", pod.Namespace, pod.Name)
		return nil, false, nil
	}

	ctx := api.WithNamespace(api.NewDefaultContext(), pod.Namespace)
	key, err := MakePodKey(ctx, pod.Name)
	if err != nil {
		return nil, false, err
	}

	//TODO(jdef) recover ports (and other resource requirements?) from the pod spec as well

	now := time.Now()
	t := &T{
		Pod:        pod,
		CreateTime: now,
		podKey:     key,
		State:      StatePending, // possibly running? mesos will tell us during reconciliation
		Flags:      make(map[FlagType]struct{}),
		mapper:     NewHostPortMapper(&pod),
		launchTime: now,
		bindTime:   now,
		Spec:       &Spec{},
	}
	var (
		offerId string
	)
	for _, k := range []string{
		annotation.BindingHostKey,
		annotation.TaskIdKey,
		annotation.SlaveIdKey,
		annotation.OfferIdKey,
	} {
		v, found := pod.Annotations[k]
		if !found {
			return nil, false, fmt.Errorf("incomplete metadata: missing value for pod annotation: %v", k)
		}
		switch k {
		case annotation.BindingHostKey:
			t.Spec.AssignedSlave = v
		case annotation.SlaveIdKey:
			t.Spec.SlaveID = v
		case annotation.OfferIdKey:
			offerId = v
		case annotation.TaskIdKey:
			t.ID = v
		case annotation.ExecutorIdKey:
			// this is nowhere near sufficient to re-launch a task, but we really just
			// want this for tracking
			t.Spec.Executor = &mesos.ExecutorInfo{ExecutorId: mutil.NewExecutorID(v)}
		}
	}
	t.Offer = offers.Expired(offerId, t.Spec.AssignedSlave, 0)
	t.Flags[Launched] = struct{}{}
	t.Flags[Bound] = struct{}{}
	return t, true, nil
}
