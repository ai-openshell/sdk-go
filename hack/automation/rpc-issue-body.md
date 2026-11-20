{{MARKER}}

{{SUMMARY}}

{{BUILDABILITY}}

{{METHODS}}

## Where core implements it

{{REFS}}

## Implementation brief

Copy this block as the task description when you start the work.

````text
{{BRIEF}}
````

## About this issue

The RPC coverage automation (`hack/automation/rpc-issues.sh`, run by the hourly core-watch workflow) opened this issue and keeps it in sync with the newest core release tag.
It rewrites this description when the definition or the buildability changes, and closes the issue when the SDK covers every method listed here under `openshell/v1`, when core removes them or makes them internal, or when they are listed with a reason in [`hack/automation/rpc-exceptions.txt`]({{EXCEPTIONS_URL}}).
An issue closed by hand stays closed. Reopen it by hand to keep it open while the automation considers the methods covered.
