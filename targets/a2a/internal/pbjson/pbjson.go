// Package pbjson converts between the A2A protocol's JSON (the shapes the
// JSON-RPC binding carries: a message, a task, a streamed event, a card) and
// the protobuf messages of the gRPC and HTTP+JSON bindings. Both the a2a
// target and the a2abind server binding speak JSON internally and convert at
// the edge, so one implementation of the protocol serves three bindings.
package pbjson

import (
	"encoding/json"
	"fmt"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2apb"
	"github.com/a2aproject/a2a-go/a2apb/pbconv"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var marshaler = protojson.MarshalOptions{UseProtoNames: false, EmitUnpopulated: false}
var unmarshaler = protojson.UnmarshalOptions{DiscardUnknown: true}

// Marshal renders a protobuf message as the HTTP+JSON binding's JSON.
func Marshal(m proto.Message) ([]byte, error) { return marshaler.Marshal(m) }

// Unmarshal parses the HTTP+JSON binding's JSON into a protobuf message.
func Unmarshal(b []byte, m proto.Message) error { return unmarshaler.Unmarshal(b, m) }

// SendRequest converts JSON MessageSendParams into a SendMessageRequest.
func SendRequest(params []byte) (*a2apb.SendMessageRequest, error) {
	var p a2a.MessageSendParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("message params: %w", err)
	}
	return pbconv.ToProtoSendMessageRequest(&p)
}

// SendParamsJSON converts a SendMessageRequest into JSON MessageSendParams.
func SendParamsJSON(req *a2apb.SendMessageRequest) ([]byte, error) {
	p, err := pbconv.FromProtoSendMessageRequest(req)
	if err != nil {
		return nil, err
	}
	return json.Marshal(p)
}

// SendResponseJSON converts a SendMessageResponse into the JSON of its task
// or message.
func SendResponseJSON(resp *a2apb.SendMessageResponse) ([]byte, error) {
	r, err := pbconv.FromProtoSendMessageResponse(resp)
	if err != nil {
		return nil, err
	}
	return json.Marshal(r)
}

// SendResponse converts the JSON of a task or message into a SendMessageResponse.
func SendResponse(result []byte) (*a2apb.SendMessageResponse, error) {
	ev, err := a2a.UnmarshalEventJSON(result)
	if err != nil {
		return nil, err
	}
	r, ok := ev.(a2a.SendMessageResult)
	if !ok {
		return nil, fmt.Errorf("a %T is not a send result", ev)
	}
	return pbconv.ToProtoSendMessageResponse(r)
}

// StreamEventJSON converts a StreamResponse into the JSON of its event.
func StreamEventJSON(resp *a2apb.StreamResponse) ([]byte, error) {
	ev, err := pbconv.FromProtoStreamResponse(resp)
	if err != nil {
		return nil, err
	}
	return json.Marshal(ev)
}

// StreamEvent converts the JSON of an event into a StreamResponse.
func StreamEvent(event []byte) (*a2apb.StreamResponse, error) {
	ev, err := a2a.UnmarshalEventJSON(event)
	if err != nil {
		return nil, err
	}
	return pbconv.ToProtoStreamResponse(ev)
}

// TaskJSON converts a Task into JSON.
func TaskJSON(t *a2apb.Task) ([]byte, error) {
	task, err := pbconv.FromProtoTask(t)
	if err != nil {
		return nil, err
	}
	return json.Marshal(task)
}

// Task converts the JSON of a task into a Task.
func Task(b []byte) (*a2apb.Task, error) {
	var t a2a.Task
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	return pbconv.ToProtoTask(&t)
}

// AgentCardJSON converts an AgentCard into JSON.
func AgentCardJSON(c *a2apb.AgentCard) ([]byte, error) {
	card, err := pbconv.FromProtoAgentCard(c)
	if err != nil {
		return nil, err
	}
	return json.Marshal(card)
}

// AgentCard converts the JSON of a card into an AgentCard.
func AgentCard(b []byte) (*a2apb.AgentCard, error) {
	var c a2a.AgentCard
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return pbconv.ToProtoAgentCard(&c)
}

// TaskPushConfigJSON converts a TaskPushNotificationConfig into the JSON of
// the JSON-RPC binding ({taskId, pushNotificationConfig}).
func TaskPushConfigJSON(c *a2apb.TaskPushNotificationConfig) ([]byte, error) {
	cfg, err := pbconv.FromProtoTaskPushConfig(c)
	if err != nil {
		return nil, err
	}
	return json.Marshal(cfg)
}

// TaskPushConfig converts {taskId, pushNotificationConfig} JSON into a
// TaskPushNotificationConfig.
func TaskPushConfig(b []byte) (*a2apb.TaskPushNotificationConfig, error) {
	var cfg a2a.TaskPushConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	return pbconv.ToProtoTaskPushConfig(&cfg)
}

// CreatePushRequest builds the request that subscribes a URL to a task's
// updates, from {taskId, pushNotificationConfig} JSON.
func CreatePushRequest(b []byte) (*a2apb.CreateTaskPushNotificationConfigRequest, error) {
	var cfg a2a.TaskPushConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	return pbconv.ToProtoCreateTaskPushConfigRequest(&cfg)
}

// CreatePushJSON converts a create request into {taskId, pushNotificationConfig} JSON.
func CreatePushJSON(req *a2apb.CreateTaskPushNotificationConfigRequest) ([]byte, error) {
	cfg, err := pbconv.FromProtoCreateTaskPushConfigRequest(req)
	if err != nil {
		return nil, err
	}
	return json.Marshal(cfg)
}

// TaskName is the resource name of a task (tasks/<id>).
func TaskName(id string) string { return pbconv.MakeTaskName(a2a.TaskID(id)) }

// TaskID reads a task id out of a resource name (tasks/<id> or tasks/<id>/...).
func TaskID(name string) (string, error) {
	id, err := pbconv.ExtractTaskID(name)
	return string(id), err
}

// ConfigID reads a push config id out of its resource name.
func ConfigID(name string) (string, error) { return pbconv.ExtractConfigID(name) }

// ConfigName is the resource name of a push config.
func ConfigName(taskID, configID string) string {
	return pbconv.MakeConfigName(a2a.TaskID(taskID), configID)
}

// GetTaskRequest builds a GetTask request.
func GetTaskRequest(id string, historyLength *int) (*a2apb.GetTaskRequest, error) {
	return pbconv.ToProtoGetTaskRequest(&a2a.TaskQueryParams{ID: a2a.TaskID(id), HistoryLength: historyLength})
}

// CancelTaskRequest builds a CancelTask request.
func CancelTaskRequest(id string) (*a2apb.CancelTaskRequest, error) {
	return pbconv.ToProtoCancelTaskRequest(&a2a.TaskIDParams{ID: a2a.TaskID(id)})
}

// SubscribeRequest builds a TaskSubscription request.
func SubscribeRequest(id string) (*a2apb.TaskSubscriptionRequest, error) {
	return pbconv.ToProtoTaskSubscriptionRequest(&a2a.TaskIDParams{ID: a2a.TaskID(id)})
}
