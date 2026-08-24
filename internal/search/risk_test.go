package search

import (
	"strings"
	"testing"
)

// A removed symbol breaks every caller, so it outranks a changed signature,
// which outranks neither.
func TestContractFactorRanksRemovalAboveResignature(t *testing.T) {
	removed := contractFactor([]RefChange{
		{Type: "removed", Name: "OrderService"},
		{Type: "modified", Name: "Other", BeforeSignature: "a()", AfterSignature: "a(int)"},
	})
	if removed.Level != RiskHigh || !strings.Contains(removed.Summary, "제거") {
		t.Errorf("removal factor = %#v, want high", removed)
	}

	resigned := contractFactor([]RefChange{
		{Type: "modified", Name: "Pay", BeforeSignature: "pay()", AfterSignature: "pay(ctx)"},
	})
	if resigned.Level != RiskMedium {
		t.Errorf("signature change factor = %#v, want medium", resigned)
	}
	if len(resigned.Evidence) == 0 || !strings.Contains(resigned.Evidence[0], "→") {
		t.Errorf("evidence must show the before and after: %#v", resigned.Evidence)
	}

	// An added symbol breaks nothing.
	added := contractFactor([]RefChange{{Type: "added", Name: "NewThing"}})
	if added.Level != RiskLow {
		t.Errorf("addition factor = %#v, want low", added)
	}

	// A modification whose signature did not change is not a contract change.
	body := contractFactor([]RefChange{
		{Type: "modified", Name: "Same", BeforeSignature: "f(a)", AfterSignature: "f(a)"},
	})
	if body.Level != RiskLow {
		t.Errorf("unchanged signature = %#v, want low", body)
	}
}

// A schema change survives a rollback of the deploy, which is what makes it
// worth flagging separately from code.
func TestSchemaFactorFlagsOnlySchemaFiles(t *testing.T) {
	flagged := schemaFactor([]RefChange{
		{Type: "modified", Name: "users", FilePath: "db/migrations/007_users.sql"},
		{Type: "modified", Name: "Service", FilePath: "internal/order/service.go"},
	})
	if flagged.Level != RiskHigh {
		t.Errorf("schema factor = %#v, want high", flagged)
	}
	if len(flagged.Evidence) != 1 || !strings.HasSuffix(flagged.Evidence[0], ".sql") {
		t.Errorf("evidence = %#v, want only the schema file", flagged.Evidence)
	}
	if !strings.Contains(flagged.Summary, "되돌아가지 않습니다") {
		t.Errorf("summary should say why schema is different: %q", flagged.Summary)
	}

	clean := schemaFactor([]RefChange{{Type: "modified", FilePath: "internal/order/service.go"}})
	if clean.Level != RiskLow {
		t.Errorf("code-only change = %#v, want low", clean)
	}
}

// The overall level is the highest factor, not an average: one dangerous factor
// stays dangerous however safe the others are.
func TestOverallLevelTakesTheWorstFactor(t *testing.T) {
	result := ChangeAssessment{
		Comparison: RefComparison{Changes: []RefChange{{Type: "removed", Name: "X"}}},
		Factors: []RiskFactor{
			{Name: "a", Level: RiskLow},
			{Name: "b", Level: RiskHigh},
			{Name: "c", Level: RiskLow},
		},
		Level: RiskLow,
	}
	for _, factor := range result.Factors {
		if factor.Level == RiskHigh {
			result.Level = RiskHigh
			break
		}
		if factor.Level == RiskMedium {
			result.Level = RiskMedium
		}
	}
	if result.Level != RiskHigh {
		t.Errorf("level = %s, want high", result.Level)
	}

	rendered := FormatAssessment(result)
	// The reasoning has to come before the verdict.
	if strings.Index(rendered, "## a") > strings.Index(rendered, "## 종합") {
		t.Errorf("the verdict was rendered before its factors:\n%s", rendered)
	}
	if !strings.Contains(rendered, "가중 합산이 아니라") {
		t.Errorf("the rendering should say how the overall level was derived:\n%s", rendered)
	}
}

func TestCappedSummarisesRatherThanTruncatingSilently(t *testing.T) {
	values := []string{"a", "b", "c", "d", "e", "f", "g"}
	got := capped(values, 5)
	if len(got) != 6 || got[5] != "외 2건" {
		t.Errorf("capped = %#v, want five entries and a count of the rest", got)
	}
	if short := capped([]string{"a"}, 5); len(short) != 1 {
		t.Errorf("a short list was altered: %#v", short)
	}
}
