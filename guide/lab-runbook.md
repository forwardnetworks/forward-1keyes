# Lab runbook: a real network end to end

A six-node [containerlab](https://containerlab.dev) network in `../lab/`:

    branch LAN 10.20.1.0/24 (ThousandEyes agent .10) -- den-rtr -- dc-rtr -- 10.30.1.0/24 (web .10)
    branch host 10.20.2.20 on den-rtr                              dc-rtr -- outside -- isp

`dc-web` listens on 80, 443 and 8080. An access list on `dc-rtr` blocks 8080 on purpose, so
one test fails for a real reason.

## What you need

- Linux with docker, containerlab and sudo, and an IOL image
  (`vrnetlab/cisco_iol`, which you build yourself from your own IOL binary).
- A ThousandEyes **Enterprise Agent account group token** (Cloud & Enterprise Agents, Agent
  Settings, Add New Enterprise Agent, Docker). Store it as `TE_AGENT_TOKEN`.
- A Forward collector that can reach the lab's management network (172.28.28.0/24).

## 1. Deploy

    set -a; . ~/.config/forward-1keyes/env; set +a
    sudo -E containerlab deploy -t lab/forward-1keyes.clab.yml

Routers take about two minutes. Check real traffic before going further:

    docker exec clab-forward-1keyes-te-agent traceroute -n 10.30.1.10
    docker exec clab-forward-1keyes-den-host sh -c 'nc -z -w 3 10.30.1.10 443; echo $?'
    docker exec clab-forward-1keyes-den-host sh -c 'nc -z -w 3 10.30.1.10 8080; echo $?'   # 1: blocked

**Keep the links at MTU 1500** (the lab file does). With containerlab's default of 9500, the
agent's link to the router stopped receiving after the first test round. The timing points at the
agent's large path probes wedging the IOL interface; that cause was not proven, and with 1500 the
link stayed up.

## 2. The agent

It registers by itself and shows in ThousandEyes within a minute or two. It reports two
addresses, the docker management address and 10.20.1.10.

## 3. A local collector

Register a collector in your Forward org (in the UI, or with a Forward admin tool); keep the
`username:password` it shows once in a mode-600 env file as `TOKEN=...`. Then:

    docker run -d --name fwdcollector-1keyes --network host --env-file collector.env \
      -e APP_HOST=<your Forward host> -e COLLECTOR_HEAP_SIZE=4 \
      -v "$PWD/logs:/collector/logs" -v fwdcollector-1keyes-private:/collector/private \
      quay.io/forwardnetworks/cloud-collector:latest

The image needs registry credentials from Forward. `COLLECTOR_HEAP_SIZE` is a bare number of
gigabytes; `4g` makes Java refuse to start, and only `docker logs` shows it.

## 4. Collect

In Forward: create a **new** network for the lab, assign the collector, add a CLI login
(containerlab's IOL default) and the two routers by their management addresses
(172.28.28.11 and .12), test connectivity, and start a collection. No device type was needed.

## 5. ThousandEyes tests

Create tests from the lab agent to 10.30.1.10: TCP 443, ICMP, HTTP on port 80 and TCP 8080, with
BGP measurements off (private destination). Wait two five-minute rounds.

## 6. Plan, apply, compare

Follow the [runbook](runbook.md), adding `--ignore-agent-subnet 172.28.28.0/24` so the management
network does not become a source. Then compare each test's loss or error in ThousandEyes with the
check's status in Forward. A comparison table from this lab is on the [guide index](README.md).

## Webhooks

To try notifications and ThousandEyes alerts through a temporary public address, see
[Webhooks](webhooks.md).

## Tear down

    sudo containerlab destroy -t lab/forward-1keyes.clab.yml
    docker stop fwdcollector-1keyes

Delete the ThousandEyes tests when you are done: they run on a schedule and use account units.
