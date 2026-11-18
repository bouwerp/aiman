package awsiac

import (
	"crypto/md5" //nolint:gosec // G501: S3 asks for Content-MD5 on configuration calls; it is an integrity check, not security
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"strconv"
)

// call is one HTTP request to the emulator, before signing.
type call struct {
	service string
	method  string
	// bucket and key make an S3 path; empty is "/".
	bucket, key string
	query       url.Values
	header      map[string]string
	body        []byte
}

// Protocol constants.
const (
	dynamoTarget = "DynamoDB_20120810."
	sqsTarget    = "AmazonSQS."
	jsonType     = "application/x-amz-json-1.0"
	formType     = "application/x-www-form-urlencoded; charset=utf-8"
	snsVersion   = "2010-03-31"
)

func jsonCall(service, target string, body any) (call, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return call{}, err
	}
	return call{service: service, method: "POST", header: map[string]string{"X-Amz-Target": target, "Content-Type": jsonType}, body: b}, nil
}

func snsCall(action string, params url.Values) call {
	params.Set("Action", action)
	params.Set("Version", snsVersion)
	return call{service: "sns", method: "POST", header: map[string]string{"Content-Type": formType}, body: []byte(canonicalQuery(params))}
}

func s3Call(method, bucket, key, sub string, body []byte, header map[string]string) call {
	c := call{service: "s3", method: method, bucket: bucket, key: key, body: body, header: map[string]string{}}
	if sub != "" {
		c.query = url.Values{sub: {""}}
	}
	for k, v := range header {
		c.header[k] = v
	}
	if body != nil && method == "PUT" && sub != "" {
		sum := md5.Sum(body) //nolint:gosec // G401: see the import
		c.header["Content-MD5"] = base64.StdEncoding.EncodeToString(sum[:])
	}
	return c
}

// resolveMap resolves an attribute map.
func (p *Plan) resolveMap(m map[string]Value) (map[string]string, error) {
	out := make(map[string]string, len(m))
	for _, k := range sortedKeys(m) {
		s, err := p.Resolve(m[k])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = s
	}
	return out, nil
}

// resourceCall builds the request of a resource step.
func (p *Plan) resourceCall(st Step, r *Resource) (call, error) {
	switch st.Operation {
	case "CreateQueue":
		attrs, err := p.resolveMap(r.Queue.Attributes)
		if err != nil {
			return call{}, err
		}
		if r.Queue.Fifo {
			attrs["FifoQueue"] = "true"
		}
		body := map[string]any{"QueueName": r.Physical}
		if len(attrs) > 0 {
			body["Attributes"] = attrs
		}
		if len(r.Queue.Tags) > 0 {
			body["tags"] = r.Queue.Tags
		}
		return jsonCall("sqs", sqsTarget+"CreateQueue", body)
	case "SetQueueAttributes":
		pol, err := p.Resolve(r.Queue.Policy)
		if err != nil {
			return call{}, err
		}
		return jsonCall("sqs", sqsTarget+"SetQueueAttributes", map[string]any{"QueueUrl": r.Attrs[AttrURL], "Attributes": map[string]string{"Policy": pol}})
	case "CreateTopic":
		attrs, err := p.resolveMap(r.Topic.Attributes)
		if err != nil {
			return call{}, err
		}
		if r.Topic.Fifo {
			attrs["FifoTopic"] = "true"
		}
		v := url.Values{"Name": {r.Physical}}
		entries(v, "Attributes", attrs)
		for i, k := range sortedKeys(r.Topic.Tags) {
			n := strconv.Itoa(i + 1)
			v.Set("Tags.member."+n+".Key", k)
			v.Set("Tags.member."+n+".Value", r.Topic.Tags[k])
		}
		return snsCall("CreateTopic", v), nil
	case "SetTopicAttributes":
		pol, err := p.Resolve(r.Topic.Policy)
		if err != nil {
			return call{}, err
		}
		return snsCall("SetTopicAttributes", url.Values{"TopicArn": {r.Attrs[AttrARN]}, "AttributeName": {"Policy"}, "AttributeValue": {pol}}), nil
	case "Subscribe":
		s := r.Subscription
		topic, err := p.Resolve(s.Topic)
		if err != nil {
			return call{}, err
		}
		endpoint, err := p.Resolve(s.Endpoint)
		if err != nil {
			return call{}, err
		}
		attrs, err := p.resolveMap(s.Attributes)
		if err != nil {
			return call{}, err
		}
		v := url.Values{"TopicArn": {topic}, "Protocol": {s.Protocol}, "Endpoint": {endpoint}, "ReturnSubscriptionArn": {"true"}}
		entries(v, "Attributes", attrs)
		return snsCall("Subscribe", v), nil
	case "CreateTable":
		return jsonCall("dynamodb", dynamoTarget+"CreateTable", createTableInput(r))
	case "UpdateTimeToLive":
		return jsonCall("dynamodb", dynamoTarget+"UpdateTimeToLive", map[string]any{
			"TableName":               r.Physical,
			"TimeToLiveSpecification": map[string]any{"Enabled": true, "AttributeName": r.Table.TTLAttribute},
		})
	case "CreateBucket":
		var body []byte
		if p.Region != "us-east-1" {
			body = mustXML(createBucketConfiguration{LocationConstraint: p.Region})
		}
		h := map[string]string{}
		if r.Bucket.ObjectLock {
			h["X-Amz-Bucket-Object-Lock-Enabled"] = "true"
		}
		return s3Call("PUT", r.Physical, "", "", body, h), nil
	case "PutBucketTagging":
		t := tagging{}
		for _, k := range sortedKeys(r.Bucket.Tags) {
			t.Tags = append(t.Tags, tag{Key: k, Value: r.Bucket.Tags[k]})
		}
		return s3Call("PUT", r.Physical, "", "tagging", mustXML(t), nil), nil
	case "PutBucketVersioning":
		return s3Call("PUT", r.Physical, "", "versioning", mustXML(versioningConfiguration{Status: r.Bucket.Versioning}), nil), nil
	case "PutBucketPolicy":
		pol, err := p.Resolve(r.Bucket.Policy)
		if err != nil {
			return call{}, err
		}
		return s3Call("PUT", r.Physical, "", "policy", []byte(pol), map[string]string{"Content-Type": "application/json"}), nil
	case "PutBucketNotificationConfiguration":
		nc, err := p.notificationConfiguration(r.Bucket)
		if err != nil {
			return call{}, err
		}
		return s3Call("PUT", r.Physical, "", "notification", mustXML(nc), nil), nil
	}
	return call{}, fmt.Errorf("awsiac: no request for %s", st.Operation)
}

// entries writes an SNS attribute map as Attributes.entry.N.key/value.
func entries(v url.Values, name string, m map[string]string) {
	for i, k := range sortedKeys(m) {
		n := strconv.Itoa(i + 1)
		v.Set(name+".entry."+n+".key", k)
		v.Set(name+".entry."+n+".value", m[k])
	}
}

func createTableInput(r *Resource) map[string]any {
	t := r.Table
	keys := func(hash, rng string) []map[string]string {
		k := []map[string]string{{"AttributeName": hash, "KeyType": "HASH"}}
		if rng != "" {
			k = append(k, map[string]string{"AttributeName": rng, "KeyType": "RANGE"})
		}
		return k
	}
	billing := t.BillingMode
	if billing == "" {
		billing = "PAY_PER_REQUEST"
	}
	throughput := func(rc, wc int64) map[string]int64 {
		if rc <= 0 {
			rc = 5
		}
		if wc <= 0 {
			wc = 5
		}
		return map[string]int64{"ReadCapacityUnits": rc, "WriteCapacityUnits": wc}
	}
	var defs []map[string]string
	for _, a := range t.Attributes {
		defs = append(defs, map[string]string{"AttributeName": a.Name, "AttributeType": a.Type})
	}
	in := map[string]any{
		"TableName": r.Physical, "KeySchema": keys(t.HashKey, t.RangeKey), "AttributeDefinitions": defs, "BillingMode": billing,
	}
	if billing == "PROVISIONED" {
		in["ProvisionedThroughput"] = throughput(t.ReadCapacity, t.WriteCapacity)
	}
	index := func(ix Index, global bool) map[string]any {
		proj := map[string]any{"ProjectionType": "ALL"}
		if ix.Projection != "" {
			proj["ProjectionType"] = ix.Projection
		}
		if len(ix.NonKeyAttributes) > 0 {
			proj["NonKeyAttributes"] = ix.NonKeyAttributes
		}
		hash := ix.HashKey
		if !global {
			hash = t.HashKey
		}
		m := map[string]any{"IndexName": ix.Name, "KeySchema": keys(hash, ix.RangeKey), "Projection": proj}
		if global && billing == "PROVISIONED" {
			m["ProvisionedThroughput"] = throughput(ix.ReadCapacity, ix.WriteCapacity)
		}
		return m
	}
	var gsis, lsis []map[string]any
	for _, ix := range t.GSIs {
		gsis = append(gsis, index(ix, true))
	}
	for _, ix := range t.LSIs {
		lsis = append(lsis, index(ix, false))
	}
	if len(gsis) > 0 {
		in["GlobalSecondaryIndexes"] = gsis
	}
	if len(lsis) > 0 {
		in["LocalSecondaryIndexes"] = lsis
	}
	if t.StreamViewType != "" {
		in["StreamSpecification"] = map[string]any{"StreamEnabled": true, "StreamViewType": t.StreamViewType}
	}
	if t.TableClass != "" {
		in["TableClass"] = t.TableClass
	}
	if t.DeletionProtection {
		in["DeletionProtectionEnabled"] = true
	}
	if len(t.Tags) > 0 {
		var tags []map[string]string
		for _, k := range sortedKeys(t.Tags) {
			tags = append(tags, map[string]string{"Key": k, "Value": t.Tags[k]})
		}
		in["Tags"] = tags
	}
	return in
}

type createBucketConfiguration struct {
	XMLName            xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ CreateBucketConfiguration"`
	LocationConstraint string   `xml:"LocationConstraint"`
}

type tag struct {
	Key   string `xml:"Key"`
	Value string `xml:"Value"`
}

type tagging struct {
	XMLName xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ Tagging"`
	Tags    []tag    `xml:"TagSet>Tag"`
}

type versioningConfiguration struct {
	XMLName xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ VersioningConfiguration"`
	Status  string   `xml:"Status"`
}

type filterRule struct {
	Name  string `xml:"Name"`
	Value string `xml:"Value"`
}

type notificationTarget struct {
	ID     string       `xml:"Id,omitempty"`
	Queue  string       `xml:"Queue,omitempty"`
	Topic  string       `xml:"Topic,omitempty"`
	Events []string     `xml:"Event"`
	Rules  []filterRule `xml:"Filter>S3Key>FilterRule,omitempty"`
}

type notificationConfiguration struct {
	XMLName xml.Name             `xml:"http://s3.amazonaws.com/doc/2006-03-01/ NotificationConfiguration"`
	Queues  []notificationTarget `xml:"QueueConfiguration"`
	Topics  []notificationTarget `xml:"TopicConfiguration"`
}

func (p *Plan) notificationConfiguration(b *Bucket) (notificationConfiguration, error) {
	var nc notificationConfiguration
	for _, n := range b.Notifications {
		arn, err := p.Resolve(n.ARN)
		if err != nil {
			return nc, err
		}
		t := notificationTarget{ID: n.ID, Events: n.Events}
		if n.Prefix != "" {
			t.Rules = append(t.Rules, filterRule{Name: "prefix", Value: n.Prefix})
		}
		if n.Suffix != "" {
			t.Rules = append(t.Rules, filterRule{Name: "suffix", Value: n.Suffix})
		}
		if n.Destination == KindQueue {
			t.Queue = arn
			nc.Queues = append(nc.Queues, t)
		} else {
			t.Topic = arn
			nc.Topics = append(nc.Topics, t)
		}
	}
	return nc, nil
}

func mustXML(v any) []byte {
	b, err := xml.Marshal(v)
	if err != nil {
		panic(err) // fixed struct types: cannot fail
	}
	return append([]byte(xml.Header), b...)
}
