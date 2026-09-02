package controllers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestMutatingControllerActionsRequirePost(t *testing.T) {
	tests := []struct {
		file    string
		actions []string
		guard   string
	}{
		{file: "index.go", actions: []string{"Add", "Copy", "Edit", "Stop", "Del", "Start", "DelHost", "HostStop", "HostStart", "AddHost", "EditHost"}, guard: "RequirePost"},
		{file: "client.go", actions: []string{"Add", "Edit", "ChangeStatus", "Del"}, guard: "RequirePost"},
		{file: "user.go", actions: []string{"Add", "Edit", "ChangeStatus", "Del"}, guard: "RequirePost"},
		{file: "global.go", actions: []string{"Save"}, guard: "RequirePost"},
		{file: "login.go", actions: []string{"Verify", "Register"}, guard: "requirePost"},
	}

	for _, test := range tests {
		parsed, err := parser.ParseFile(token.NewFileSet(), test.file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", test.file, err)
		}
		for _, action := range test.actions {
			if !functionCalls(parsed, action, test.guard) {
				t.Errorf("%s.%s must call %s before mutating state", test.file, action, test.guard)
			}
		}
	}
}

func TestAdministrativeClientActionsRequireAdmin(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "client.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"ChangeStatus", "Del"} {
		if !functionCalls(parsed, action, "RequireAdmin") {
			t.Errorf("client.go.%s must require administrator authorization", action)
		}
	}
	global, err := parser.ParseFile(token.NewFileSet(), "global.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !functionCalls(global, "Index", "RequireAdmin") {
		t.Fatal("global.go.Index must require administrator authorization")
	}
}

func functionCalls(file *ast.File, functionName, methodName string) bool {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != functionName || function.Body == nil {
			continue
		}
		called := false
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if ok && selector.Sel.Name == methodName {
				called = true
			}
			return true
		})
		return called
	}
	return false
}
