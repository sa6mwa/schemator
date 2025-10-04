// This example package demonstrates the use of generators with the schemator
// package (the intended use-case), explore by running: go generate.
package example

import (
	"time"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Example demonstrates how schemator is an abstraction to
// github.com/inopop/jsonschema building JSON schema files from comments on
// structs and fields.
type Example struct {
	// Unique identifier for this entity.
	UUID uuid.UUID `json:"uuid"`
	// A subject identifies an entity in the system.
	Subject Subject `json:"subject"`
	// Metadata is an example utilizing a model from an external module.
	ObjectMeta metav1.ObjectMeta `json:"objectMeta"`
}

type Subject struct {
	// ID is the ID of the subject.
	ID int `json:"id"`
	// Full name of subject (Name Surname).
	Name string `json:"name"`
	// Tags associated with the subject.
	Tags        []string  `json:"tags"`
	DateOfBirth time.Time `json:"dateOfBirth"`
}
