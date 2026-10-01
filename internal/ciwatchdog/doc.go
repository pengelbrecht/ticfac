// Package ciwatchdog holds the tests of .github/scripts/main-ci-watchdog.sh,
// the scheduled check that starts CI on a main head GitHub never ran it on, so
// a merged fix cannot silently never deploy (deploy-factory.yml runs on CI's
// workflow_run). It has no Go code of its own.
package ciwatchdog
