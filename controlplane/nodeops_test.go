package controlplane

import (
	"reflect"
	"testing"
)

func TestNodeRequestsAreTakenOnceAndDeduplicated(t *testing.T) {
	var q nodeRequests
	q.add(&q.images, "n1", "redis:7", "redis:7", "pg:16")
	q.add(&q.restart, "n1", "p/api/latest")
	restart, images := q.take("n1")
	if !reflect.DeepEqual(images, []string{"redis:7", "pg:16"}) || !reflect.DeepEqual(restart, []string{"p/api/latest"}) {
		t.Fatalf("restart %v images %v", restart, images)
	}
	if r, i := q.take("n1"); r != nil || i != nil {
		t.Fatalf("a second take should be empty, got %v %v", r, i)
	}
}
