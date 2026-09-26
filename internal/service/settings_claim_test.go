package service

import (
	"testing"

	"github.com/rake-pro/dropwarden/internal/config"
)

// The claim selector is a pointer: the default round-trips as nil, an explicit
// "" sticks (capture only) and is not replaced by the default on save.
func TestClaimSelectorRoundTrip(t *testing.T) {
	cfg, _ := config.StarterDefaults()
	applyConfigDTO(cfg, toConfigDTO(cfg))
	if cfg.Claim.InventoryButtonSelector != nil {
		t.Fatalf("saving the default must leave the selector unset, got %q", *cfg.Claim.InventoryButtonSelector)
	}
	d := toConfigDTO(cfg)
	empty := ""
	d.ClaimButtonSelector = &empty
	applyConfigDTO(cfg, d)
	if cfg.ClaimInventorySelector() != "" {
		t.Fatalf("explicit empty selector should stick, got %q", cfg.ClaimInventorySelector())
	}
	d = toConfigDTO(cfg)
	if d.ClaimButtonSelector == nil || *d.ClaimButtonSelector != "" {
		t.Fatalf("DTO should carry the empty selector, got %v", d.ClaimButtonSelector)
	}
	applyConfigDTO(cfg, d)
	if cfg.ClaimInventorySelector() != "" {
		t.Fatalf("re-saving must keep the empty selector, got %q", cfg.ClaimInventorySelector())
	}
}
