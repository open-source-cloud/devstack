package resource

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// This file is the LocalStack (SQS/SNS) Provisioner (spec 29 §messaging — the
// OPT-IN cloud backend selected via --engine sqs|sns). It creates/inspects/deletes
// SQS QUEUES (kind=queue) and SNS TOPICS (kind=topic) using the PURE-GO
// aws-sdk-go-v2 SQS + SNS clients in-process (CGO-free, so they stay inside the
// single static binary — like the MinIO S3 provisioner), pointed at the LocalStack
// endpoint (the localstack template's `provides: aws`). The SDK sits behind the
// small SQSAPI/SNSAPI seams so unit/race tests run without a live LocalStack:
// inject the factories with fakes. Queue/topic names are transparently
// PROJECT-PREFIXED for tenant isolation; callers escape with --no-prefix. Idempotent.

// SQSAPI is the subset of the aws-sdk-go-v2 SQS client the provisioner uses.
type SQSAPI interface {
	CreateQueue(context.Context, *sqs.CreateQueueInput, ...func(*sqs.Options)) (*sqs.CreateQueueOutput, error)
	GetQueueUrl(context.Context, *sqs.GetQueueUrlInput, ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error)
	GetQueueAttributes(context.Context, *sqs.GetQueueAttributesInput, ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
	DeleteQueue(context.Context, *sqs.DeleteQueueInput, ...func(*sqs.Options)) (*sqs.DeleteQueueOutput, error)
	ListQueues(context.Context, *sqs.ListQueuesInput, ...func(*sqs.Options)) (*sqs.ListQueuesOutput, error)
}

// SNSAPI is the subset of the aws-sdk-go-v2 SNS client the provisioner uses.
type SNSAPI interface {
	CreateTopic(context.Context, *sns.CreateTopicInput, ...func(*sns.Options)) (*sns.CreateTopicOutput, error)
	DeleteTopic(context.Context, *sns.DeleteTopicInput, ...func(*sns.Options)) (*sns.DeleteTopicOutput, error)
	ListTopics(context.Context, *sns.ListTopicsInput, ...func(*sns.Options)) (*sns.ListTopicsOutput, error)
	Subscribe(context.Context, *sns.SubscribeInput, ...func(*sns.Options)) (*sns.SubscribeOutput, error)
}

// SQSFactory / SNSFactory build the admin clients for a resolved Target (the
// 127.0.0.1 overlay endpoint + dev creds). Injectable so the provisioner is
// endpoint-free in tests; nil selects the real aws-sdk-go-v2 client.
type (
	SQSFactory func(ctx context.Context, t Target) (SQSAPI, error)
	SNSFactory func(ctx context.Context, t Target) (SNSAPI, error)
)

// LocalStack is the LocalStack (SQS/SNS) Provisioner. It is keyed by the shared
// TEMPLATE name "localstack" (what ResolveInstance matches on) while the SQS/SNS
// clients target the template's `provides: aws` endpoint on :4566. Factories nil →
// the real clients.
type LocalStack struct {
	SQSFactory SQSFactory
	SNSFactory SNSFactory
}

var _ Provisioner = LocalStack{}

// Engine reports the shared-template name this provisioner serves ("localstack";
// the template's `provides: aws` is reached over the same instance's endpoint).
func (LocalStack) Engine() string { return "localstack" }

// Kinds are the resource kinds this provisioner can create.
func (LocalStack) Kinds() []string { return []string{"queue", "topic"} }

func awsEndpoint(t Target) string { return fmt.Sprintf("http://%s:%d", t.Host, t.Port) }

func (l LocalStack) sqsClient(ctx context.Context, t Target) (SQSAPI, error) {
	if l.SQSFactory != nil {
		return l.SQSFactory(ctx, t)
	}
	cfg := awsDevConfig(t)
	return sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(awsEndpoint(t)) }), nil
}

func (l LocalStack) snsClient(ctx context.Context, t Target) (SNSAPI, error) {
	if l.SNSFactory != nil {
		return l.SNSFactory(ctx, t)
	}
	cfg := awsDevConfig(t)
	return sns.NewFromConfig(cfg, func(o *sns.Options) { o.BaseEndpoint = aws.String(awsEndpoint(t)) }), nil
}

// awsDevConfig builds the LocalStack dev config (the "test"/"test" cred convention,
// or the instance's params). Mirrors the MinIO defaultS3Client pattern.
func awsDevConfig(t Target) aws.Config {
	access := t.AdminEnv["user"]
	if access == "" {
		access = "test"
	}
	secret := t.AdminEnv["password"]
	if secret == "" {
		secret = "test"
	}
	return aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(access, secret, ""),
	}
}

// Ensure idempotently provisions an SQS queue (kind=queue) or SNS topic
// (kind=topic). For queues: --fifo appends .fifo + sets FifoQueue, and a
// Params["dlq"] (already-provisioned DLQ physical name) wires a redrive policy with
// Params["max_receive"]. For topics: an optional Params["subscribe"] (an SQS queue
// physical name) wires the classic SNS→SQS fan-out. Returns the connection facts.
func (l LocalStack) Ensure(ctx context.Context, t Target, r Resource) (Attrs, error) {
	switch r.Kind {
	case "queue":
		return l.ensureQueue(ctx, t, r)
	case "topic":
		return l.ensureTopic(ctx, t, r)
	default:
		return nil, fmt.Errorf("aws engine does not support kind %q", r.Kind)
	}
}

func (l LocalStack) ensureQueue(ctx context.Context, t Target, r Resource) (Attrs, error) {
	c, err := l.sqsClient(ctx, t)
	if err != nil {
		return nil, err
	}
	name := r.Name
	if name == "" {
		name = r.Owner
	}
	attrsMap := map[string]string{}
	if boolParam(r.Params, "fifo") {
		if !strings.HasSuffix(name, ".fifo") {
			name += ".fifo"
		}
		attrsMap["FifoQueue"] = "true"
	}
	// A DLQ is created first (by the caller, as its own recorded resource); here we
	// resolve its ARN and set the redrive policy on the main queue.
	if dlq := paramStr(r.Params, "dlq"); dlq != "" {
		arn, err := l.queueArn(ctx, c, dlq)
		if err != nil {
			return nil, fmt.Errorf("resolve DLQ %q arn: %w", dlq, err)
		}
		maxRecv := intParam(r.Params, "max_receive")
		if maxRecv <= 0 {
			maxRecv = 5
		}
		attrsMap["RedrivePolicy"] = fmt.Sprintf(`{"deadLetterTargetArn":%q,"maxReceiveCount":"%d"}`, arn, maxRecv)
	}
	out, err := c.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName:  aws.String(name),
		Attributes: attrsMap,
	})
	if err != nil {
		return nil, fmt.Errorf("create sqs queue %q: %w", name, err)
	}
	return Attrs{
		"endpoint": awsEndpoint(t),
		"host":     sharedHost(t.Instance),
		"port":     "4566",
		"queue":    name,
		"queueUrl": aws.ToString(out.QueueUrl),
	}, nil
}

func (l LocalStack) ensureTopic(ctx context.Context, t Target, r Resource) (Attrs, error) {
	c, err := l.snsClient(ctx, t)
	if err != nil {
		return nil, err
	}
	name := r.Name
	if name == "" {
		name = r.Owner
	}
	out, err := c.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String(name)})
	if err != nil {
		return nil, fmt.Errorf("create sns topic %q: %w", name, err)
	}
	topicArn := aws.ToString(out.TopicArn)
	attrs := Attrs{
		"endpoint": awsEndpoint(t),
		"host":     sharedHost(t.Instance),
		"port":     "4566",
		"topic":    name,
		"topicArn": topicArn,
	}
	if sub := paramStr(r.Params, "subscribe"); sub != "" {
		sqsC, err := l.sqsClient(ctx, t)
		if err != nil {
			return nil, err
		}
		arn, err := l.queueArn(ctx, sqsC, sub)
		if err != nil {
			return nil, fmt.Errorf("resolve subscribe queue %q arn: %w", sub, err)
		}
		if _, err := c.Subscribe(ctx, &sns.SubscribeInput{
			TopicArn: aws.String(topicArn),
			Protocol: aws.String("sqs"),
			Endpoint: aws.String(arn),
		}); err != nil {
			return nil, fmt.Errorf("subscribe %q to topic %q: %w", sub, name, err)
		}
		attrs["subscribed"] = sub
	}
	return attrs, nil
}

// queueArn resolves the ARN of an existing SQS queue by physical name.
func (LocalStack) queueArn(ctx context.Context, c SQSAPI, name string) (string, error) {
	u, err := c.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
	if err != nil {
		return "", err
	}
	attr, err := c.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       u.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	if err != nil {
		return "", err
	}
	return attr.Attributes[string(sqstypes.QueueAttributeNameQueueArn)], nil
}

// Drop removes the SQS queue or SNS topic. Idempotent: a missing object is not an
// error. Never touches the shared LocalStack container.
func (l LocalStack) Drop(ctx context.Context, t Target, r Resource) error {
	name := r.Name
	if name == "" {
		name = r.Owner
	}
	switch r.Kind {
	case "queue":
		c, err := l.sqsClient(ctx, t)
		if err != nil {
			return err
		}
		u, err := c.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
		if err != nil {
			if isAwsNotFound(err) {
				return nil
			}
			return fmt.Errorf("resolve sqs queue %q: %w", name, err)
		}
		if _, err := c.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: u.QueueUrl}); err != nil {
			if isAwsNotFound(err) {
				return nil
			}
			return fmt.Errorf("delete sqs queue %q: %w", name, err)
		}
		return nil
	case "topic":
		c, err := l.snsClient(ctx, t)
		if err != nil {
			return err
		}
		arn, err := l.topicArn(ctx, c, name)
		if err != nil {
			return fmt.Errorf("resolve sns topic %q: %w", name, err)
		}
		if arn == "" {
			return nil // already gone
		}
		if _, err := c.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: aws.String(arn)}); err != nil {
			if isAwsNotFound(err) {
				return nil
			}
			return fmt.Errorf("delete sns topic %q: %w", name, err)
		}
		return nil
	}
	return fmt.Errorf("aws engine cannot drop kind %q", r.Kind)
}

// Preflight verifies the endpoint is reachable (a ListQueues round-trip).
func (l LocalStack) Preflight(ctx context.Context, t Target) error {
	c, err := l.sqsClient(ctx, t)
	if err != nil {
		return err
	}
	_, err = c.ListQueues(ctx, &sqs.ListQueuesInput{})
	return err
}

// ListQueues returns the tenant's SQS queue names (prefix-filtered). Lock-free read.
func (l LocalStack) ListQueues(ctx context.Context, t Target, prefix string) ([]string, error) {
	c, err := l.sqsClient(ctx, t)
	if err != nil {
		return nil, err
	}
	in := &sqs.ListQueuesInput{}
	if prefix != "" {
		in.QueueNamePrefix = aws.String(prefix)
	}
	out, err := c.ListQueues(ctx, in)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, u := range out.QueueUrls {
		names = append(names, queueNameFromURL(u))
	}
	sort.Strings(names)
	return names, nil
}

// ListTopics returns the tenant's SNS topic names (prefix-filtered). Lock-free read.
func (l LocalStack) ListTopics(ctx context.Context, t Target, prefix string) ([]string, error) {
	c, err := l.snsClient(ctx, t)
	if err != nil {
		return nil, err
	}
	out, err := c.ListTopics(ctx, &sns.ListTopicsInput{})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, tp := range out.Topics {
		name := topicNameFromARN(aws.ToString(tp.TopicArn))
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// topicArn resolves an SNS topic ARN by physical name via ListTopics (LocalStack
// exposes no GetTopicByName).
func (LocalStack) topicArn(ctx context.Context, c SNSAPI, name string) (string, error) {
	out, err := c.ListTopics(ctx, &sns.ListTopicsInput{})
	if err != nil {
		return "", err
	}
	for _, tp := range out.Topics {
		arn := aws.ToString(tp.TopicArn)
		if topicNameFromARN(arn) == name {
			return arn, nil
		}
	}
	return "", nil
}

// queueNameFromURL extracts the queue name (the last path segment) from a queue URL.
func queueNameFromURL(u string) string {
	if i := strings.LastIndexByte(u, '/'); i >= 0 {
		return u[i+1:]
	}
	return u
}

// topicNameFromARN extracts the topic name (the last colon segment) from a topic ARN.
func topicNameFromARN(arn string) string {
	if i := strings.LastIndexByte(arn, ':'); i >= 0 {
		return arn[i+1:]
	}
	return arn
}

// isAwsNotFound reports the SQS/SNS "does not exist" family so Drop stays idempotent.
func isAwsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var qne *sqstypes.QueueDoesNotExist
	if errors.As(err, &qne) {
		return true
	}
	code := apiCode(err)
	switch code {
	case "AWS.SimpleQueueService.NonExistentQueue", "NotFound", "ResourceNotFoundException", "QueueDoesNotExist":
		return true
	}
	e := strings.ToLower(err.Error())
	return strings.Contains(e, "nonexistent") || strings.Contains(e, "does not exist") || strings.Contains(e, "not found")
}
