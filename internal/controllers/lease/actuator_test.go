/*
Copyright The ORC Authors.

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

package lease

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/gophercloud/gophercloud/v2/openstack/reservation/v1/leases"
	"k8s.io/utils/ptr"

	orcv1alpha1 "github.com/k-orc/openstack-resource-controller/v3/api/v1alpha1"
	orcerrors "github.com/k-orc/openstack-resource-controller/v3/internal/util/errors"
	orcapplyconfigv1alpha1 "github.com/k-orc/openstack-resource-controller/v3/pkg/clients/applyconfiguration/api/v1alpha1"
)

func TestReservationOpts(t *testing.T) {
	testCases := []struct {
		name        string
		reservation orcv1alpha1.LeaseReservation
		expected    leases.ReservationOptsBuilder
	}{
		{
			name: "Host",
			reservation: orcv1alpha1.LeaseReservation{
				Host: &orcv1alpha1.LeaseHostReservation{
					Min:                  1,
					Max:                  2,
					HypervisorProperties: ptr.To(`[">=", "$vcpus", "4"]`),
				},
			},
			expected: leases.HostReservationOpts{
				Min:                  1,
				Max:                  2,
				HypervisorProperties: `[">=", "$vcpus", "4"]`,
			},
		},
		{
			name: "Instance",
			reservation: orcv1alpha1.LeaseReservation{
				Instance: &orcv1alpha1.LeaseInstanceReservation{
					Amount:             3,
					Vcpus:              2,
					MemoryMB:           4096,
					DiskGB:             ptr.To[int32](0),
					Affinity:           ptr.To(false),
					ResourceProperties: ptr.To(`["==", "$gpu", "a100"]`),
				},
			},
			expected: leases.InstanceReservationOpts{
				Amount:             3,
				VCPUs:              2,
				MemoryMB:           4096,
				DiskGB:             0,
				Affinity:           ptr.To(false),
				ResourceProperties: `["==", "$gpu", "a100"]`,
			},
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := reservationOpts(&tt.reservation).ToReservationMap()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			expected, err := tt.expected.ToReservationMap()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for k, v := range expected {
				if ptrVal, ok := v.(*bool); ok {
					if gotVal, _ := got[k].(*bool); ptr.Deref(gotVal, true) != ptr.Deref(ptrVal, true) {
						t.Errorf("%s: expected %v, got %v", k, ptr.Deref(ptrVal, true), ptr.Deref(gotVal, true))
					}
					continue
				}
				if got[k] != v {
					t.Errorf("%s: expected %v, got %v", k, v, got[k])
				}
			}
		})
	}
}

func TestPendingPollingPeriod(t *testing.T) {
	testCases := []struct {
		name       string
		untilStart time.Duration
		expected   time.Duration
	}{
		{name: "Start date passed", untilStart: -time.Minute, expected: leaseAvailablePollingPeriod},
		{name: "Starting soon", untilStart: time.Second, expected: leaseAvailablePollingPeriod},
		{name: "Starting within the maximum", untilStart: 5 * time.Minute, expected: 5 * time.Minute},
		{name: "Starting in days", untilStart: 72 * time.Hour, expected: leasePendingMaxPollingPeriod},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			if got := pendingPollingPeriod(tt.untilStart); got != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, got)
			}
		})
	}
}

func TestCheckStatus(t *testing.T) {
	testCases := []struct {
		status       string
		wantTerminal bool
	}{
		{status: LeaseStatusPending},
		{status: LeaseStatusActive},
		{status: LeaseStatusTerminated, wantTerminal: true},
		{status: LeaseStatusError, wantTerminal: true},
	}

	for _, tt := range testCases {
		t.Run(tt.status, func(t *testing.T) {
			reconcileStatus := leaseActuator{}.checkStatus(context.TODO(), nil, &osResourceT{Status: tt.status})
			if isTerminal(reconcileStatus.GetError()) != tt.wantTerminal {
				t.Errorf("expected terminal: %v, got: %v", tt.wantTerminal, reconcileStatus.GetError())
			}
		})
	}
}

func TestCreateResourceNameTooLong(t *testing.T) {
	obj := &orcv1alpha1.Lease{}
	obj.Name = strings.Repeat("a", leaseNameMaxLength+1)
	obj.Spec.Resource = &orcv1alpha1.LeaseResourceSpec{}

	_, reconcileStatus := leaseActuator{}.CreateResource(context.TODO(), obj)
	if !isTerminal(reconcileStatus.GetError()) {
		t.Errorf("expected a terminal error, got: %v", reconcileStatus.GetError())
	}
}

func isTerminal(err error) bool {
	var terminalErr *orcerrors.TerminalError
	return errors.As(err, &terminalErr)
}

func TestApplyResourceStatusReservationIDs(t *testing.T) {
	osResource := &osResourceT{
		Reservations: []leases.Reservation{
			{ID: "host-reservation", ResourceType: leases.ResourceTypeHost},
			{
				ID:            "instance-reservation",
				ResourceType:  leases.ResourceTypeInstance,
				FlavorID:      ptr.To("instance-reservation"),
				ServerGroupID: ptr.To("server-group"),
			},
		},
	}

	statusApply := orcapplyconfigv1alpha1.LeaseStatus()
	leaseStatusWriter{}.ApplyResourceStatus(logr.Discard(), osResource, statusApply)

	reservations := statusApply.Resource.Reservations
	if len(reservations) != 2 {
		t.Fatalf("expected 2 reservations, got %d", len(reservations))
	}
	// A host reservation has no flavor or server group, and must not report
	// empty IDs for them
	if reservations[0].FlavorID != nil || reservations[0].ServerGroupID != nil {
		t.Errorf("expected no flavor or server group for a host reservation, got %v and %v",
			reservations[0].FlavorID, reservations[0].ServerGroupID)
	}
	if ptr.Deref(reservations[1].FlavorID, "") != "instance-reservation" ||
		ptr.Deref(reservations[1].ServerGroupID, "") != "server-group" {
		t.Errorf("expected flavor and server group for an instance reservation, got %v and %v",
			reservations[1].FlavorID, reservations[1].ServerGroupID)
	}
}
