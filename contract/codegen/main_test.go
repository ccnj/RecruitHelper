package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestLocalHandshakeContractRejectsLegacyFields(t *testing.T) {
	raw, err := os.ReadFile("../contract.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}
	if err := validateLocalHandshakeContract(base); err != nil {
		t.Fatalf("当前契约应通过本地握手门禁: %v", err)
	}

	tests := []struct {
		name string
		want string
		edit func(map[string]any)
	}{
		{
			name: "hello auth",
			want: "HelloBody.auth 已退役",
			edit: func(c map[string]any) {
				typeFields(c, "HelloBody")["auth"] = map[string]any{"type": "string"}
			},
		},
		{
			name: "nullable handId",
			want: "handId 必须是 required non-null string",
			edit: func(c map[string]any) {
				typeFields(c, "HelloBody")["handId"].(map[string]any)["nullable"] = true
			},
		},
		{
			name: "issued credentials type",
			want: "IssuedCreds 已退役",
			edit: func(c map[string]any) {
				contractTypes(c)["IssuedCreds"] = map[string]any{"type": "object", "fields": map[string]any{}}
			},
		},
		{
			name: "welcome issued",
			want: "WelcomeBody.issued 已退役",
			edit: func(c map[string]any) {
				typeFields(c, "WelcomeBody")["issued"] = map[string]any{"ref": "IssuedCreds", "optional": true}
			},
		},
		{
			name: "pairing default",
			want: "pairingWindowMs 已随配对流程退役",
			edit: func(c map[string]any) {
				c["defaults"].(map[string]any)["pairingWindowMs"] = float64(60000)
			},
		},
		{
			name: "auth bye code",
			want: "AUTH_FAILED 已随配对/鉴权流程退役",
			edit: func(c map[string]any) {
				c["byeCodes"] = append(c["byeCodes"].([]any), "AUTH_FAILED")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutant := cloneContract(t, base)
			tt.edit(mutant)
			err := validateLocalHandshakeContract(mutant)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("期望拒绝并包含 %q,得到 %v", tt.want, err)
			}
		})
	}
}

func cloneContract(t *testing.T, in map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func contractTypes(c map[string]any) map[string]any {
	return c["schemas"].(map[string]any)["types"].(map[string]any)
}

func typeFields(c map[string]any, name string) map[string]any {
	return contractTypes(c)[name].(map[string]any)["fields"].(map[string]any)
}
