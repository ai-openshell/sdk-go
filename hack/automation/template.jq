# SPDX-License-Identifier: Apache-2.0
#
# template.jq: fill the {{KEY}} placeholders of a template, shared by
# rpc-issues.jq and fill-template.sh. Load it with
# `jq -L hack/automation 'include "template"; ...'`.

# fill($vals): the input string with every {{KEY}} replaced by $vals[KEY], in
# one pass. Inserted text is never scanned again, so a value that contains
# "{{...}}" (text from core, say) stays as is. A placeholder that $vals has
# no value for is an error, so a typo in a template cannot render as empty.
def fill($vals):
  gsub("\\{\\{(?<k>[A-Z_]+)\\}\\}"; $vals[.k] // error("unknown placeholder {{\(.k)}}"));
