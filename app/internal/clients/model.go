// Package clients implements the client (customer) domain on top of a
// DynamoDB-compatible API, served here by ExtendDB.
//
// Single-table design for table "Clients":
//
//	pk              sk                 entity
//	CLIENT#<id>     PROFILE            client profile (gsi1pk = EMAIL#<email>)
//	CLIENT#<id>     ADDRESS#<kind>     postal address (billing, shipping, ...)
//	CLIENT#<id>     CONTACT#<kind>     contact channel (phone, email, ...)
//
// GSI "gsi1" (gsi1pk, gsi1sk) supports looking a client up by e-mail.
package clients

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"
)

const (
	clientPrefix  = "CLIENT#"
	emailPrefix   = "EMAIL#"
	profileSK     = "PROFILE"
	addressPrefix = "ADDRESS#"
	contactPrefix = "CONTACT#"

	StatusActive = "ACTIVE"
)

// Client is the aggregate returned by the API.
type Client struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Document  string    `json:"document"`
	Segment   string    `json:"segment"`
	Status    string    `json:"status"`
	HomeCell  string    `json:"homeCell"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Addresses []Address `json:"addresses,omitempty"`
	Contacts  []Contact `json:"contacts,omitempty"`
}

type Address struct {
	Kind       string `json:"kind"`
	Street     string `json:"street"`
	City       string `json:"city"`
	State      string `json:"state"`
	Country    string `json:"country"`
	PostalCode string `json:"postalCode"`
}

type Contact struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// ValidationError reports a client that breaks a domain rule.
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}

// Validate normalizes and checks a client before it is written.
func (c *Client) Validate() error {
	c.Name = strings.TrimSpace(c.Name)
	c.Email = strings.ToLower(strings.TrimSpace(c.Email))
	if c.Name == "" {
		return invalid("name is required")
	}
	if !strings.Contains(c.Email, "@") {
		return invalid("a valid email is required")
	}
	for i, a := range c.Addresses {
		if a.Kind == "" {
			return invalid("addresses[%d].kind is required", i)
		}
	}
	for i, ct := range c.Contacts {
		if ct.Kind == "" || ct.Value == "" {
			return invalid("contacts[%d].kind and value are required", i)
		}
	}
	return nil
}

// profileItem, addressItem and contactItem are the stored shapes.
type profileItem struct {
	PK         string    `dynamodbav:"pk"`
	SK         string    `dynamodbav:"sk"`
	GSI1PK     string    `dynamodbav:"gsi1pk"`
	GSI1SK     string    `dynamodbav:"gsi1sk"`
	EntityType string    `dynamodbav:"entityType"`
	ClientID   string    `dynamodbav:"clientId"`
	Name       string    `dynamodbav:"name"`
	Email      string    `dynamodbav:"email"`
	Document   string    `dynamodbav:"document,omitempty"`
	Segment    string    `dynamodbav:"segment,omitempty"`
	Status     string    `dynamodbav:"status"`
	HomeCell   string    `dynamodbav:"homeCell"`
	CreatedAt  time.Time `dynamodbav:"createdAt"`
	UpdatedAt  time.Time `dynamodbav:"updatedAt"`
}

type addressItem struct {
	PK         string `dynamodbav:"pk"`
	SK         string `dynamodbav:"sk"`
	EntityType string `dynamodbav:"entityType"`
	Address
}

type contactItem struct {
	PK         string `dynamodbav:"pk"`
	SK         string `dynamodbav:"sk"`
	EntityType string `dynamodbav:"entityType"`
	Contact
}

func clientPK(id string) string { return clientPrefix + id }

func toProfileItem(c *Client) profileItem {
	return profileItem{
		PK:         clientPK(c.ID),
		SK:         profileSK,
		GSI1PK:     emailPrefix + c.Email,
		GSI1SK:     clientPK(c.ID),
		EntityType: "Client",
		ClientID:   c.ID,
		Name:       c.Name,
		Email:      c.Email,
		Document:   c.Document,
		Segment:    c.Segment,
		Status:     c.Status,
		HomeCell:   c.HomeCell,
		CreatedAt:  c.CreatedAt,
		UpdatedAt:  c.UpdatedAt,
	}
}

func (p profileItem) toClient() *Client {
	return &Client{
		ID:        p.ClientID,
		Name:      p.Name,
		Email:     p.Email,
		Document:  p.Document,
		Segment:   p.Segment,
		Status:    p.Status,
		HomeCell:  p.HomeCell,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
}

// NewID returns a random RFC 4122 version 4 UUID.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
