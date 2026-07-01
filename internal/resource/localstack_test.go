package resource

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// fakeSQS embeds SQSAPI (nil) so it satisfies the interface; only the used methods
// are overridden. It records queues + their attributes.
type fakeSQS struct {
	SQSAPI
	queues  map[string]map[string]string // name → attributes (incl. RedrivePolicy)
	created []string
}

func newFakeSQS() *fakeSQS { return &fakeSQS{queues: map[string]map[string]string{}} }

func (f *fakeSQS) CreateQueue(_ context.Context, in *sqs.CreateQueueInput, _ ...func(*sqs.Options)) (*sqs.CreateQueueOutput, error) {
	name := aws.ToString(in.QueueName)
	f.created = append(f.created, name)
	f.queues[name] = in.Attributes
	return &sqs.CreateQueueOutput{QueueUrl: aws.String("http://localhost:4566/000000000000/" + name)}, nil
}
func (f *fakeSQS) GetQueueUrl(_ context.Context, in *sqs.GetQueueUrlInput, _ ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error) {
	name := aws.ToString(in.QueueName)
	if _, ok := f.queues[name]; !ok {
		return nil, &sqstypes.QueueDoesNotExist{}
	}
	return &sqs.GetQueueUrlOutput{QueueUrl: aws.String("http://localhost:4566/000000000000/" + name)}, nil
}
func (f *fakeSQS) GetQueueAttributes(_ context.Context, in *sqs.GetQueueAttributesInput, _ ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	name := queueNameFromURL(aws.ToString(in.QueueUrl))
	return &sqs.GetQueueAttributesOutput{Attributes: map[string]string{
		string(sqstypes.QueueAttributeNameQueueArn): "arn:aws:sqs:us-east-1:000000000000:" + name,
	}}, nil
}
func (f *fakeSQS) DeleteQueue(_ context.Context, in *sqs.DeleteQueueInput, _ ...func(*sqs.Options)) (*sqs.DeleteQueueOutput, error) {
	delete(f.queues, queueNameFromURL(aws.ToString(in.QueueUrl)))
	return &sqs.DeleteQueueOutput{}, nil
}
func (f *fakeSQS) ListQueues(_ context.Context, in *sqs.ListQueuesInput, _ ...func(*sqs.Options)) (*sqs.ListQueuesOutput, error) {
	var urls []string
	for n := range f.queues {
		if in.QueueNamePrefix != nil && !strings.HasPrefix(n, aws.ToString(in.QueueNamePrefix)) {
			continue
		}
		urls = append(urls, "http://localhost:4566/000000000000/"+n)
	}
	return &sqs.ListQueuesOutput{QueueUrls: urls}, nil
}

// fakeSNS embeds SNSAPI (nil); records topics + subscriptions.
type fakeSNS struct {
	SNSAPI
	topics []string
	subs   []string
}

func (f *fakeSNS) CreateTopic(_ context.Context, in *sns.CreateTopicInput, _ ...func(*sns.Options)) (*sns.CreateTopicOutput, error) {
	name := aws.ToString(in.Name)
	if !slices.Contains(f.topics, name) {
		f.topics = append(f.topics, name)
	}
	return &sns.CreateTopicOutput{TopicArn: aws.String("arn:aws:sns:us-east-1:000000000000:" + name)}, nil
}
func (f *fakeSNS) ListTopics(_ context.Context, _ *sns.ListTopicsInput, _ ...func(*sns.Options)) (*sns.ListTopicsOutput, error) {
	var out []snstypes.Topic
	for _, n := range f.topics {
		out = append(out, snstypes.Topic{TopicArn: aws.String("arn:aws:sns:us-east-1:000000000000:" + n)})
	}
	return &sns.ListTopicsOutput{Topics: out}, nil
}
func (f *fakeSNS) DeleteTopic(_ context.Context, in *sns.DeleteTopicInput, _ ...func(*sns.Options)) (*sns.DeleteTopicOutput, error) {
	name := topicNameFromARN(aws.ToString(in.TopicArn))
	f.topics = slices.DeleteFunc(f.topics, func(s string) bool { return s == name })
	return &sns.DeleteTopicOutput{}, nil
}
func (f *fakeSNS) Subscribe(_ context.Context, in *sns.SubscribeInput, _ ...func(*sns.Options)) (*sns.SubscribeOutput, error) {
	f.subs = append(f.subs, aws.ToString(in.Endpoint))
	return &sns.SubscribeOutput{SubscriptionArn: aws.String("sub-arn")}, nil
}

func awsTarget() Target {
	return Target{Instance: "localstack", Host: "127.0.0.1", Port: 44566, AdminEnv: map[string]string{"user": "test", "password": "test"}}
}

func localstackWith(sq *fakeSQS, sn *fakeSNS) LocalStack {
	return LocalStack{
		SQSFactory: func(context.Context, Target) (SQSAPI, error) { return sq, nil },
		SNSFactory: func(context.Context, Target) (SNSAPI, error) { return sn, nil },
	}
}

func TestLocalStackEngineAndKinds(t *testing.T) {
	l := LocalStack{}
	if l.Engine() != "localstack" {
		t.Errorf("Engine() = %q, want localstack (the template name; provides: aws)", l.Engine())
	}
	if !slices.Contains(l.Kinds(), "queue") || !slices.Contains(l.Kinds(), "topic") {
		t.Errorf("Kinds() = %v", l.Kinds())
	}
}

func TestLocalStackSQSCreateFifoAndDLQRedrive(t *testing.T) {
	sq := newFakeSQS()
	l := localstackWith(sq, &fakeSNS{})
	ctx := context.Background()
	// DLQ first (as the CLI does), then the FIFO main queue with a redrive policy.
	if _, err := l.Ensure(ctx, awsTarget(), Resource{Engine: "aws", Kind: "queue", Name: "web-jobs-dead", Owner: "web"}); err != nil {
		t.Fatalf("create DLQ: %v", err)
	}
	attrs, err := l.Ensure(ctx, awsTarget(), Resource{Engine: "aws", Kind: "queue", Name: "web-jobs", Owner: "web",
		Params: map[string]any{"fifo": true, "dlq": "web-jobs-dead", "max_receive": 5}})
	if err != nil {
		t.Fatalf("create main queue: %v", err)
	}
	// FIFO appends .fifo and sets FifoQueue=true.
	if !slices.Contains(sq.created, "web-jobs.fifo") {
		t.Errorf("FIFO queue not created with .fifo suffix: %v", sq.created)
	}
	main := sq.queues["web-jobs.fifo"]
	if main["FifoQueue"] != "true" {
		t.Errorf("FifoQueue attr = %q, want true", main["FifoQueue"])
	}
	if rp := main["RedrivePolicy"]; !strings.Contains(rp, "web-jobs-dead") || !strings.Contains(rp, `"maxReceiveCount":"5"`) {
		t.Errorf("redrive policy wrong: %q", rp)
	}
	if attrs["queue"] != "web-jobs.fifo" {
		t.Errorf("attrs[queue] = %q", attrs["queue"])
	}
}

func TestLocalStackSNSCreateAndSubscribe(t *testing.T) {
	sq := newFakeSQS()
	sn := &fakeSNS{}
	l := localstackWith(sq, sn)
	ctx := context.Background()
	// The SQS queue to subscribe must exist first.
	_, _ = l.Ensure(ctx, awsTarget(), Resource{Engine: "aws", Kind: "queue", Name: "web-jobs", Owner: "web"})
	attrs, err := l.Ensure(ctx, awsTarget(), Resource{Engine: "aws", Kind: "topic", Name: "web-events", Owner: "web",
		Params: map[string]any{"subscribe": "web-jobs"}})
	if err != nil {
		t.Fatalf("create topic: %v", err)
	}
	if !slices.Contains(sn.topics, "web-events") {
		t.Errorf("topic not created: %v", sn.topics)
	}
	if len(sn.subs) != 1 || !strings.Contains(sn.subs[0], "web-jobs") {
		t.Errorf("SNS→SQS subscription not wired: %v", sn.subs)
	}
	if attrs["subscribed"] != "web-jobs" {
		t.Errorf("attrs[subscribed] = %q", attrs["subscribed"])
	}
}

func TestLocalStackDropAndListPrefix(t *testing.T) {
	sq := newFakeSQS()
	sn := &fakeSNS{}
	l := localstackWith(sq, sn)
	ctx := context.Background()
	for _, n := range []string{"web-a", "api-b"} {
		_, _ = l.Ensure(ctx, awsTarget(), Resource{Engine: "aws", Kind: "queue", Name: n, Owner: "x"})
	}
	got, err := l.ListQueues(ctx, awsTarget(), "web-")
	if err != nil {
		t.Fatalf("ListQueues: %v", err)
	}
	if !slices.Equal(got, []string{"web-a"}) {
		t.Errorf("ListQueues(web-) = %v, want [web-a]", got)
	}
	// Drop is idempotent (missing queue → no error).
	if err := l.Drop(ctx, awsTarget(), Resource{Engine: "aws", Kind: "queue", Name: "web-a", Owner: "web"}); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if err := l.Drop(ctx, awsTarget(), Resource{Engine: "aws", Kind: "queue", Name: "web-a", Owner: "web"}); err != nil {
		t.Errorf("Drop of missing queue must be idempotent: %v", err)
	}
}
