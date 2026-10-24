package controllers

import (
	"ehang.io/nps/lib/acl"
)

func validateSourceAccessLists(scope acl.Scope, priority int, black, white []string) error {
	_, err := acl.CompileSource(scope, priority, black, white)
	return err
}
