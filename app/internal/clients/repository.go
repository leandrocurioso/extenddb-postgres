package clients

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// batchWriteLimit is DynamoDB's maximum number of requests per BatchWriteItem.
const batchWriteLimit = 25

var (
	ErrNotFound     = errors.New("client not found")
	ErrClientExists = errors.New("client already exists")
)

type Repository struct {
	db    *dynamodb.Client
	table string
	log   *slog.Logger
}

func NewRepository(db *dynamodb.Client, table string, log *slog.Logger) *Repository {
	return &Repository{db: db, table: table, log: log}
}

// EnsureTable creates the Clients table if it is missing and waits until it is
// ACTIVE. Every cell runs this at startup, so losing the CreateTable race to
// another cell (ResourceInUseException) is expected and harmless.
func (r *Repository) EnsureTable(ctx context.Context) error {
	_, err := r.db.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: &r.table})
	var notFound *types.ResourceNotFoundException
	switch {
	case err == nil:
	case errors.As(err, &notFound):
		r.log.Info("creating table", "table", r.table)
		_, err = r.db.CreateTable(ctx, &dynamodb.CreateTableInput{
			TableName:   &r.table,
			BillingMode: types.BillingModePayPerRequest,
			AttributeDefinitions: []types.AttributeDefinition{
				{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS},
				{AttributeName: aws.String("sk"), AttributeType: types.ScalarAttributeTypeS},
				{AttributeName: aws.String("gsi1pk"), AttributeType: types.ScalarAttributeTypeS},
				{AttributeName: aws.String("gsi1sk"), AttributeType: types.ScalarAttributeTypeS},
			},
			KeySchema: []types.KeySchemaElement{
				{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash},
				{AttributeName: aws.String("sk"), KeyType: types.KeyTypeRange},
			},
			GlobalSecondaryIndexes: []types.GlobalSecondaryIndex{{
				IndexName: aws.String("gsi1"),
				KeySchema: []types.KeySchemaElement{
					{AttributeName: aws.String("gsi1pk"), KeyType: types.KeyTypeHash},
					{AttributeName: aws.String("gsi1sk"), KeyType: types.KeyTypeRange},
				},
				Projection: &types.Projection{ProjectionType: types.ProjectionTypeAll},
			}},
		})
		var inUse *types.ResourceInUseException
		if err != nil && !errors.As(err, &inUse) {
			return fmt.Errorf("create table %s: %w", r.table, err)
		}
	default:
		return fmt.Errorf("describe table %s: %w", r.table, err)
	}

	waiter := dynamodb.NewTableExistsWaiter(r.db)
	return waiter.Wait(ctx, &dynamodb.DescribeTableInput{TableName: &r.table}, 2*time.Minute)
}

// PutClient stores the client profile with PutItem. The condition makes the
// call create-only, so two cells can never silently overwrite the same client.
func (r *Repository) PutClient(ctx context.Context, c *Client) error {
	item, err := attributevalue.MarshalMap(toProfileItem(c))
	if err != nil {
		return err
	}
	_, err = r.db.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           &r.table,
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	var condFailed *types.ConditionalCheckFailedException
	if errors.As(err, &condFailed) {
		return ErrClientExists
	}
	return err
}

// WriteClientDetails writes the client's addresses and contacts with
// BatchWriteItem.
func (r *Repository) WriteClientDetails(ctx context.Context, c *Client) error {
	var reqs []types.WriteRequest
	for _, a := range c.Addresses {
		item, err := attributevalue.MarshalMap(addressItem{
			PK: clientPK(c.ID), SK: addressPrefix + strings.ToUpper(a.Kind), EntityType: "Address", Address: a,
		})
		if err != nil {
			return err
		}
		reqs = append(reqs, types.WriteRequest{PutRequest: &types.PutRequest{Item: item}})
	}
	for _, ct := range c.Contacts {
		item, err := attributevalue.MarshalMap(contactItem{
			PK: clientPK(c.ID), SK: contactPrefix + strings.ToUpper(ct.Kind), EntityType: "Contact", Contact: ct,
		})
		if err != nil {
			return err
		}
		reqs = append(reqs, types.WriteRequest{PutRequest: &types.PutRequest{Item: item}})
	}
	return r.batchWrite(ctx, reqs)
}

// BatchPutClients bulk-imports client profiles with BatchWriteItem. Unlike
// PutClient it is not conditional: an existing profile is replaced.
func (r *Repository) BatchPutClients(ctx context.Context, cs []*Client) error {
	reqs := make([]types.WriteRequest, 0, len(cs))
	for _, c := range cs {
		item, err := attributevalue.MarshalMap(toProfileItem(c))
		if err != nil {
			return err
		}
		reqs = append(reqs, types.WriteRequest{PutRequest: &types.PutRequest{Item: item}})
	}
	return r.batchWrite(ctx, reqs)
}

func (r *Repository) batchWrite(ctx context.Context, reqs []types.WriteRequest) error {
	for start := 0; start < len(reqs); start += batchWriteLimit {
		pending := map[string][]types.WriteRequest{r.table: reqs[start:min(start+batchWriteLimit, len(reqs))]}
		for attempt := 0; len(pending) > 0; attempt++ {
			if attempt > 0 {
				if attempt > 8 {
					return fmt.Errorf("batch write: %d items still unprocessed", len(pending[r.table]))
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(50<<attempt) * time.Millisecond):
				}
			}
			out, err := r.db.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{RequestItems: pending})
			if err != nil {
				return err
			}
			pending = out.UnprocessedItems
		}
	}
	return nil
}

// GetClient reads the profile and all child items with a single Query on the
// client's partition.
func (r *Repository) GetClient(ctx context.Context, id string) (*Client, error) {
	p := dynamodb.NewQueryPaginator(r.db, &dynamodb.QueryInput{
		TableName:                 &r.table,
		KeyConditionExpression:    aws.String("pk = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": &types.AttributeValueMemberS{Value: clientPK(id)}},
		ConsistentRead:            aws.Bool(true),
	})
	var (
		c         *Client
		addresses []Address
		contacts  []Contact
	)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			sk := item["sk"].(*types.AttributeValueMemberS).Value
			switch {
			case sk == profileSK:
				var pi profileItem
				if err := attributevalue.UnmarshalMap(item, &pi); err != nil {
					return nil, err
				}
				c = pi.toClient()
			case strings.HasPrefix(sk, addressPrefix):
				var ai addressItem
				if err := attributevalue.UnmarshalMap(item, &ai); err != nil {
					return nil, err
				}
				addresses = append(addresses, ai.Address)
			case strings.HasPrefix(sk, contactPrefix):
				var ci contactItem
				if err := attributevalue.UnmarshalMap(item, &ci); err != nil {
					return nil, err
				}
				contacts = append(contacts, ci.Contact)
			}
		}
	}
	if c == nil {
		return nil, ErrNotFound
	}
	c.Addresses, c.Contacts = addresses, contacts
	return c, nil
}

// FindByEmail resolves a client through the gsi1 e-mail index.
func (r *Repository) FindByEmail(ctx context.Context, email string) (*Client, error) {
	out, err := r.db.Query(ctx, &dynamodb.QueryInput{
		TableName:              &r.table,
		IndexName:              aws.String("gsi1"),
		KeyConditionExpression: aws.String("gsi1pk = :e"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":e": &types.AttributeValueMemberS{Value: emailPrefix + strings.ToLower(strings.TrimSpace(email))},
		},
		Limit: aws.Int32(1),
	})
	if err != nil {
		return nil, err
	}
	if len(out.Items) == 0 {
		return nil, ErrNotFound
	}
	var pi profileItem
	if err := attributevalue.UnmarshalMap(out.Items[0], &pi); err != nil {
		return nil, err
	}
	return r.GetClient(ctx, pi.ClientID)
}
