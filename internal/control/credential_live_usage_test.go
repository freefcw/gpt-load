package control

import (
	"encoding/json"
	"testing"

	"gpt-load/internal/channel"
	"gpt-load/internal/ratelimit"
)

func TestCredentialCollectionIncludesLiveLimitUsage(t *testing.T) {
	fixture := newServiceFixture(t)
	created, err := fixture.service.CreateGroup(t.Context(), GroupCreateRequest{
		ChannelID: channel.OpenAI, Params: json.RawMessage(`{}`),
		Models:      optionalGroupModels{Set: true, Values: []GroupModel{{ID: "gpt-4o"}}},
		Credentials: "sk-live-usage", ConnectionType: "api_key",
	})
	if err != nil {
		t.Fatal(err)
	}
	query := CredentialCollectionQuery{Page: 1, PageSize: 20}
	collection, err := fixture.service.ListGroupCredentials(t.Context(), created.GroupID, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(collection.Items) != 1 {
		t.Fatalf("credentials = %d, want 1", len(collection.Items))
	}
	id := collection.Items[0].CredentialID
	if collection.Items[0].RPMUsed != 0 || collection.Items[0].ConcurrencyUsed != 0 {
		t.Fatalf("usage without limiter = %d/%d, want 0/0", collection.Items[0].RPMUsed, collection.Items[0].ConcurrencyUsed)
	}

	limiter := ratelimit.NewCredentialLimiter()
	fixture.service.SetCredentialLimitUsage(limiter)
	release, ok := limiter.Acquire(id, 4, 2)
	if !ok {
		t.Fatal("acquire rejected")
	}
	defer release()
	if _, ok := limiter.Acquire(id, 4, 2); !ok {
		t.Fatal("second acquire rejected")
	}

	collection, err = fixture.service.ListGroupCredentials(t.Context(), created.GroupID, query)
	if err != nil {
		t.Fatal(err)
	}
	item := collection.Items[0]
	if item.CredentialID != id || item.RPMUsed != 2 || item.ConcurrencyUsed != 2 {
		t.Fatalf("collection usage = id %d rpm %d concurrency %d, want %d 2/2", item.CredentialID, item.RPMUsed, item.ConcurrencyUsed, id)
	}
	detail, err := fixture.service.loadCredentialItem(t.Context(), created.GroupID, id)
	if err != nil {
		t.Fatal(err)
	}
	if detail.RPMUsed != 2 || detail.ConcurrencyUsed != 2 {
		t.Fatalf("detail usage = %d/%d, want 2/2", detail.RPMUsed, detail.ConcurrencyUsed)
	}
}
