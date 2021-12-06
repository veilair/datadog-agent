// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package pb

// From https://github.com/DataDog/libddwaf/blob/8ee1ae3111d525357040c0e06a86d2667d6df490/schema/appsec-event-1.0.0.json
type AppSecRule struct {
	ID   string            `msg:"id"`
	Name string            `msg:"name"`
	Tags map[string]string `msg:"tags"`
}

type AppSecRuleMatchParameter struct {
	Address   string        `msg:"address"`
	KeyPath   []interface{} `msg:"key_path"`
	Value     string        `msg:"value"`
	Highlight []string      `msg:"highlight"`
}

type AppSecRuleMatch struct {
	Operator      string                     `msg:"operator"`
	OperatorValue string                     `msg:"operator_value"`
	Parameters    []AppSecRuleMatchParameter `msg:"parameters"`
}

type AppSecTrigger struct {
	Rule        AppSecRule        `msg:"rule"`
	RuleMatches []AppSecRuleMatch `msg:"rule_matches"`
}

type AppSecStruct struct {
	Triggers []AppSecTrigger `msg:"triggers"`
}
