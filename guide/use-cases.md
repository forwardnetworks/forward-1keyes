# Use cases

ThousandEyes measures what traffic does. Forward models what the network allows, from device
configuration and state. Each status below is one of:

- **Built**: in this tool today.
- **Shown by hand**: done once in the lab with the two products' own data; not automated.
- **Proposed**: not built. Facts about the other system are cited where checked.

## ThousandEyes to Forward

### 1. Tests become intent checks — Built

Each agent-to-server, HTTP server or agent-to-agent test becomes one Forward check per source
prefix and destination address. Forward re-evaluates it on every snapshot. Result: what you
already monitor becomes what the network must keep allowing.

### 2. Two-sided triage — Built

`report` puts each check's latest ThousandEyes measurement next to its status in Forward:

| ThousandEyes | Forward | Verdict | What it suggests |
| :--- | :--- | :--- | :--- |
| working | PASS | healthy | nothing to do |
| failing | FAIL | both-blocked | the model does not allow it either; start with the path in Forward |
| failing | PASS | network-allows | the model allows it: look at the service, circuit, live device state or an old snapshot |
| working | FAIL | model-disagrees | check the source address, then collect the network again |

Anything else is inconclusive. It only reads. Agent-to-agent checks are not compared yet. In the
lab it showed healthy and both-blocked on real traffic; see the
[runbook](runbook.md#compare-what-was-measured-with-what-is-modelled).

### 3. Change assurance — Proposed (not tested with these checks)

Because the checks are ordinary Forward intent checks, a Forward change prediction that re-runs
checks should include them, so a change that would break a monitored flow shows before it is
made. This has not been tried with checks this tool made.

### 4. Alert enrichment by webhook — Proposed

ThousandEyes can POST a custom webhook when an alert triggers or clears, with a templated body.
[Variables](https://docs.thousandeyes.com/product-documentation/integration-guides/custom-webhooks/webhook-vars.md)
include the test id (`alert.test.id`), alert id and rule, and the trigger or clear type. Failed
deliveries are retried
([docs](https://docs.thousandeyes.com/product-documentation/integration-guides/custom-webhooks.md)).

A small receiver would:

1. accept the POST and authenticate it;
2. find the managed checks whose contributors include that test id;
3. read their status on the latest snapshot, and for a FAIL the Forward path;
4. post one answer to chat or a ticket: "the model also blocks this, at this hop" or "the model
   allows this path; look beyond the network configuration".

It only reads Forward. To work it needs an HTTPS address ThousandEyes can reach. How a
ThousandEyes webhook can authenticate itself to the receiver still has to be checked.

## Forward to ThousandEyes

### 5. Re-plan on a new snapshot — Built (run it on a timer or your own hook)

`changes` counts what `plan` would do now: new tests without checks, changed and stale checks,
collisions and tests that cannot be imported. It reads only; `apply` stays with a person. The tool
runs no server, so a timer, or whatever handles a Forward snapshot webhook, has to start it.

### 6. Notify when a managed check flips — Built

`changes --notify` lists the checks that broke or recovered between two snapshots, names the
ThousandEyes tests behind each, and posts one signed message. In the lab, blocking a port produced
one "broke" and removing the block one "recovered"; see the
[runbook](runbook.md#what-changed-since-the-last-snapshot-and-a-notification). It was tested against
a receiver on the same machine, not against a particular chat product.

### 7. Create ThousandEyes tests from Forward checks — Proposed

For flows Forward verifies but nothing measures, create tests. ThousandEyes supports creating
tests by API
([docs](https://docs.thousandeyes.com/product-documentation/api/create-update-delete-tests-using-the-thousandeyes-api.md)),
which needs an account-admin level role. This writes to ThousandEyes, uses account units and needs
an agent near each source, so it should be a reviewed plan-and-apply like the other direction.
The tool today only reads ThousandEyes.

### 8. Compare paths hop by hop — Proposed

The lab agent's traceroute matched the Forward path. ThousandEyes has path-visualization results
through its test-results API; the list call returned path ids live, and reading hop detail has not
been tried. A comparison would flag where the measured path leaves the modelled one.

## What does not look possible

- **Pushing Forward results into ThousandEyes as events or annotations.** No inbound event or
  annotation API was found in the ThousandEyes documentation index. Dashboards are writable by
  API, but that is not an event feed.
- **Writing ThousandEyes tags by API** to mark tests that have checks: not confirmed either way.

## Suggested order

1. Alert enrichment receiver (4): needs a reachable endpoint and an authentication decision. It can
   answer with the verdict that `report` computes.
2. Test creation (7): first feature that writes to ThousandEyes.
3. Hop-by-hop path comparison (8).
