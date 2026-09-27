package gql

import (
	"errors"
	"testing"
)

const integrityDetails = `{"errors":[{"message":"failed integrity check","path":["user","dropCampaign"],"extensions":{"code":"IntegrityCheckFailed"}}],"data":{"user":{"dropCampaign":null}}}`
const integrityCampaigns = `{"errors":[{"message":"failed integrity check","path":["currentUser","dropCampaigns"],"extensions":{"code":"IntegrityCheckFailed"}}],"data":{"currentUser":{"id":"1","login":"me","dropCampaigns":null}}}`

func TestIntegrityRejectionIsTyped(t *testing.T) {
	if _, _, err := ParseCampaignDetails([]byte(integrityDetails)); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("details: want ErrIntegrity, got %v", err)
	}
	if _, err := ParseCampaigns([]byte(integrityCampaigns)); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("campaigns: want ErrIntegrity, got %v", err)
	}
	if _, err := ParseClaimDropRewards([]byte(`{"errors":[{"message":"failed integrity check"}],"data":null}`)); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("claim: want ErrIntegrity, got %v", err)
	}
	if !IsIntegrityFailure([]byte("[" + integrityCampaigns + "]")) {
		t.Fatal("batched integrity body must be recognized")
	}
	if IsIntegrityFailure([]byte(`{"data":{"user":{"dropCampaign":null}}}`)) {
		t.Fatal("a plain null must not read as an integrity failure")
	}
}
