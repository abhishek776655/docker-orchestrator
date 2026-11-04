# haco web console

React, TypeScript, Vite and Tailwind v4, with shadcn-style components. It talks to the control plane's operator API (`/v1/*`).

## Run

Start a control plane (it listens on `127.0.0.1:8099` by default), then:

```bash
cd web
npm install
npm run dev
```

Vite proxies `/v1` to the control plane. Point it elsewhere with `HACO_API`, for example `HACO_API=http://127.0.0.1:18099 npm run dev`.

```bash
npm run build   # typecheck and production build into web/dist
```

## What is built

- **Cluster** (`/`): the control-plane host at the center with agent nodes around it. Hover a machine for its details, click to open it. "Add node" issues a join token and shows the `hacoctl agent join` command. Projects and recent events sit below.
- Nodes, Projects, Deploy and Events are placeholders.

## Data notes

- Nodes show their OS from the `os` label and the host from the `haco.io/role=host` label. Agents publish both at registration. A node registered by an older agent shows a plain monitor with no OS mark.
- CPU and memory "reserved" are summed on the client from workloads placed on each node, using the same rule as `store.ActiveReservations` (everything except failed or stopped).
- The page polls every 3 seconds. There is no event stream yet.
