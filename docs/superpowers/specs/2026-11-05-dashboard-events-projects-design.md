# Web console: Dashboard, Cluster, Projects, Events and notification bell

Date: 2026-11-05. Scope: `web/` console and its design canvas. No control-plane API changes.

## Problem

The cluster page currently carries three jobs: topology, the project table and recent events. The original Overview (summary tiles, attention items) was replaced by the topology page and never built in the real app. Events have no home beyond a short list, and nothing alerts the operator to a failure while they are on another page.

## Decisions

- Events live in three places: a notification bell for new problems, the dashboard for the latest few, and a full Events page for history.
- The project table moves to a Projects page.
- The Overview returns as the Dashboard and is the home route.
- The topology page keeps only the topology.

## Routes and navigation

| Route | Page | Sidebar group |
| --- | --- | --- |
| `/` | Dashboard | Platform |
| `/cluster` | Cluster topology | Platform |
| `/nodes`, `/nodes/:id` | Placeholders (unchanged) | Platform |
| `/projects` | Projects table | Platform |
| `/projects/:name` | Project detail | Platform (Projects stays active) |
| `/deploy` | Placeholder (unchanged) | Platform |
| `/events` | Events history | Operate |

Sidebar order: Dashboard, Cluster, Nodes, Projects, Deploy, then Events under Operate. The logo links to `/`. Topology node clicks still go to `/nodes/:id` and the host card to `/deploy`.

## Dashboard (`/`)

- **Tiles (4):**
  - Schedulable nodes: count of nodes in state `available` or `throttling`, out of all nodes.
  - Workloads ready: workloads with desired state running and status `ready`, out of all desired-running workloads.
  - Failed: desired-running workloads with status `failed`.
  - In progress: desired-running workloads whose status is not `ready`, `failed` or `stopped`.
- **Needs attention:** a list built by `deriveAttention`. Items, in this order:
  1. Each failed desired-running workload: title `project/service`, detail is the status reason, links to `/projects`.
  2. Each node in state `offline`: detail is the `offlineHint` text, links to `/nodes/:id`.
  3. Each node in state `throttling`: detail is the temperature, links to `/nodes/:id`.
  4. Each node in state `cordoned`: detail "No new workloads", links to `/nodes/:id`.
  Empty state: "Nothing needs attention."
- **Latest events:** the 5 newest events using the shared event row, with a "View all" link to `/events`.
- No project table and no topology on this page.

## Cluster (`/cluster`)

Current topology page minus the Projects and Recent events sections. The header summary line, legend, topology, hover cards and Add node dialog are unchanged.

## Projects (`/projects`)

The existing table (project, services, status badges, revisions) with the same derivation (`deriveProjects`). Loading and empty states as today. The project name and an "Open" link on each row go to `/projects/:name`.

## Project detail (`/projects/:name`)

- **Header:** breadcrumb `Projects / name`, project name, one status badge per status count, a revision picker, and actions Stop all, Redeploy (links to `/deploy`) and Remove. Stop all and Remove are confirmed actions; their API wiring is a later step, so the first build shows them disabled.
- **Summary tiles (3):** service count (with the revision when all match), total reserved CPU and memory, and the nodes in use.
- **Services table:** service, status badge, node, reserved CPU and memory, and what it waits for (`depends_on` with condition). Each row opens the service page.
- **Dependencies:** the layered graph from the Deploy page with live status, the "no cycles" pill and a one-line explanation of what is blocked.
- **Project events:** the project's newest events with a link to the full Events page.
- The service page breadcrumb becomes `Projects / name / service`, with the name linking back here.

## Events (`/events`)

- Fetches the 200 newest events.
- Filter chips: All, Failures, Warnings. The filter is client-side on `eventTone`: Failures is tone `bad`, Warnings is tone `warn`.
- Each row is the shared event row: type badge, relative time, message, and the workload key when present.
- The list scrolls with the page; there is no inner scroll box.

## Notification bell

- Placed at the right of the top bar on every page.
- **Unseen count:** number of events with an ID greater than the stored last-seen ID whose tone is `bad` or `warn`. The badge shows the count, capped at "9+", and is hidden at zero.
- **Last-seen ID:** stored in `localStorage` under `haco.events.seenId`. If storage is unavailable, last-seen is held in memory for the session. On first visit (nothing stored) it is set to the newest event ID so history does not flood the badge.
- **Popover:** a Radix Popover listing the 8 newest events of any tone, with a "View all events" link to `/events`. Opening it sets last-seen to the newest event ID, which clears the badge.
- The button has an accessible name that includes the count ("Notifications, 3 new").

## Shared data layer

`web/src/lib/queries.ts` exports `useNodes`, `useWorkloads` and `useEvents`. Each wraps `useQuery` with a fixed key and poll interval (3s for nodes and workloads, 5s for events). `useEvents` always fetches 200 events; consumers slice (bell 8, dashboard 5). Pages stop declaring their own queries. The Add node dialog is unchanged.

## New and changed files (web/)

- `src/lib/queries.ts` (new), `src/lib/attention.ts` (new: `deriveAttention`, `unseenCount`, tile counts).
- `src/pages/dashboard.tsx`, `projects.tsx`, `events.tsx` (new); `cluster.tsx` trimmed.
- `src/components/notification-bell.tsx`, `event-row.tsx`, `ui/popover.tsx` (new).
- `src/components/app-shell.tsx`: sidebar items, top bar with bell.
- `src/App.tsx`: routes. `@radix-ui/react-popover` added.

## Design canvas

- `Main.dc.html` becomes the Dashboard board (tiles, attention list, latest events, bell in the top bar).
- New `Cluster.dc.html` holds the current topology design without the projects and events sections.
- New `Projects.dc.html` holds the project table.
- New `Project.dc.html` holds the project detail page; the Projects table, the Dashboard's "Open project" links and the Service board breadcrumb point to it.
- All boards: sidebar gains Dashboard and points Cluster at `Cluster.dc.html`; top bar gains the bell.
- No Events board; the Events page reuses the dashboard event row.

## Testing

- Add `vitest` as a dev dependency with unit tests for the pure functions: `deriveAttention` (ordering, empty state), `unseenCount` (tone filtering, ID comparison, first-visit baseline) and the tile counts.
- `npm run typecheck` and `npm run build` must pass.
- Manual check in the browser against a throwaway control plane with seeded failed, throttling, cordoned and no-heartbeat nodes: badge count and clearing, attention list links, Events filters, and the bell popover.

## Out of scope

- Server-pushed events or sockets; polling stays.
- Per-user read state on the server.
- Node detail, Service detail and Deploy pages, and wiring Stop all and Remove on the project page.
- Network discovery of devices.
