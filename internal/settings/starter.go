package settings

// questionsStarter is the commented prt.questions.toml EnsureQuestions
// writes. It holds no live question, so it loads as the built-in catalogue.
const questionsStarter = `# prt.questions.toml — your additions to the Jev question catalogue.
#
# The built-in questions (spec "Jev question catalogue") always apply. Each
# [[question]] here is merged over them by (scope, id):
#   - a new scope+id ADDS a question;
#   - a built-in scope+id REDEFINES that question (the whole row is replaced);
#   - a built-in scope+id with deferred = "off" DEFERS it (never asked).
#
# Fields:
#   scope    line | unit | file | group | pair | item | branch
#   id       slug [a-z0-9_]+ (no "__")
#   type     N (boolean), C (choice), S (score)
#   prompt   one factor, written for a judgment model reading a code diff
#   options  C: the choice values; S: rungs lowest -> highest;
#            N: empty, or "<sub>: <description>" per sub-question
#   criteria optional descriptions: C option -> description; N "true"/"false"
#   inputs   extra state beyond the unit (pr_title, callers, role, ...)
#   feeds    what consumes the answer; required (feeds = [] is rejected)
#   deferred any non-empty value switches the question off
#
# Changes take effect when the daemon restarts; a changed catalogue re-asks
# the affected labels (that spends Jev calls).
#
# Example (remove the leading "# " to use it):
#
# [[question]]
# scope = "unit"
# id = "touches_feature_flag"
# type = "C"
# prompt = "Does this change add, remove or read a feature flag?"
# options = ["adds", "removes", "reads", "none"]
# inputs = ["pr_title"]
# feeds = ["tags"]
`
