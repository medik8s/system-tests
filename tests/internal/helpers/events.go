package helpers

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

// EventExpectation describes a Kubernetes Event to assert on.
// Reason is required. Type and MessageSubstr are optional filters.
type EventExpectation struct {
	Reason        string
	Type          string
	MessageSubstr string
}

// InvolvedObjectRef identifies the object whose events to query.
// For cluster-scoped objects (Node), leave Namespace empty.
// UID is optional; when set it narrows the field selector to avoid
// matching events from a same-named but different object.
type InvolvedObjectRef struct {
	Kind      string
	Name      string
	Namespace string
	UID       string
}

// WaitForEvents polls the API server until all expected events are found
// for the given involved object, or until the timeout expires.
func WaitForEvents(
	ctx context.Context,
	clientset kubernetes.Interface,
	involved InvolvedObjectRef,
	expected []EventExpectation,
	timeout, interval time.Duration,
) error {
	if involved.Kind == "" || involved.Name == "" {
		return fmt.Errorf("WaitForEvents: Kind and Name are required in InvolvedObjectRef")
	}

	if len(expected) == 0 {
		return fmt.Errorf("WaitForEvents: expected events list must not be empty")
	}

	var (
		lastMissing []string
		lastListErr error
	)

	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	err := wait.PollUntilContextCancel(pollCtx, interval, true,
		func(ctx context.Context) (bool, error) {
			events, listErr := ListEventsForObject(ctx, clientset, involved)
			if listErr != nil {
				lastListErr = listErr

				return false, nil
			}

			lastListErr = nil

			lastMissing = lastMissing[:0]

			for _, expectation := range expected {
				if !hasMatchingEvent(events, expectation) {
					lastMissing = append(lastMissing, fmt.Sprintf(
						"reason=%q type=%q", expectation.Reason, expectation.Type))
				}
			}

			if len(lastMissing) > 0 {
				return false, nil
			}

			return true, nil
		},
	)
	if err != nil {
		var details []string
		if len(lastMissing) > 0 {
			details = append(details, fmt.Sprintf(
				"missing events for %s/%s: [%s]", involved.Kind, involved.Name,
				strings.Join(lastMissing, ", ")))
		}

		if lastListErr != nil {
			details = append(details, fmt.Sprintf("last API error: %v", lastListErr))
		}

		if len(details) > 0 {
			return fmt.Errorf("%w; %s", err, strings.Join(details, "; "))
		}
	}

	return err
}

// ListEventsForObject fetches Kubernetes Events for the given involved object
// using field selectors on the API server.
func ListEventsForObject(
	ctx context.Context,
	clientset kubernetes.Interface,
	involved InvolvedObjectRef,
) ([]corev1.Event, error) {
	selectorSet := fields.Set{
		"involvedObject.kind": involved.Kind,
		"involvedObject.name": involved.Name,
	}

	if involved.UID != "" {
		selectorSet["involvedObject.uid"] = involved.UID
	}

	selector := fields.SelectorFromSet(selectorSet)

	list, err := clientset.CoreV1().Events(involved.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: selector.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list events for %s/%s: %w",
			involved.Kind, involved.Name, err)
	}

	return list.Items, nil
}

func hasMatchingEvent(events []corev1.Event, expectation EventExpectation) bool {
	for i := range events {
		if eventMatches(&events[i], expectation) {
			return true
		}
	}

	return false
}

func eventMatches(event *corev1.Event, expectation EventExpectation) bool {
	if event.Reason != expectation.Reason {
		return false
	}

	if expectation.Type != "" && event.Type != expectation.Type {
		return false
	}

	if expectation.MessageSubstr != "" {
		return strings.Contains(event.Message, expectation.MessageSubstr)
	}

	return true
}
