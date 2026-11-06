Move the core pin from `{{OLD}}` to `{{NEW}}` ({{KIND}}).

- Core changes: [{{OLD}}...{{NEW}}]({{COMPARE_URL}})
- Regenerated bindings: {{GENERATED}}
- E2E runs against `{{IMAGE}}`
- Conformance fixtures:
{{FIXTURES}}

## Proto changes

{{PROTO_DIFF}}

## Before merging

- CI and E2E must pass. If the SDK needs changes for this core version, push them to this branch.
- Merging a stable pin tags the SDK release `{{NEW}}`. Pre-release pins are not tagged; a newer pre-release proposal replaces this one.
