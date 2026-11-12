# AI Labs and Blueprints for Humans and Agents

**Learn it. Provision it. Make it yours.**

**`insta`** is the **Instaclustr CLI** for AI-assisted learning, infrastructure provisioning, and blueprinting. It brings hands-on AI workshops into a repeatable developer workflow: start with a working lab, deploy the services it needs, understand how the solution works, and use an AI coding assistant to adapt the implementation to your own use case.

```text
Discover a lab  →  Create a project  →  Provision  →  Learn by doing  →  Adapt with AI  →  Clean up
```

Think of `insta` as a bridge between **learning AI architectures** and **building with them**:

- **Learn:** Explore guided labs for data retrieval (RAG), agentic solutions, and the intersection of technology and everything AI. Work through the exercises yourself or ask an assistant to explain the code and design decisions.
- **Provision:** Create and manage the Instaclustr infrastructure required by a lab, starting with managed OpenSearch. The CLI handles project configuration, readiness checks, declared task execution, and cleanup.
- **Blueprint:** Use the lab as a starting point for your application. With an optional Codex session, investigate the existing implementation, discuss tradeoffs, and make changes for your data, requirements, and workflow.

The `insta` uses familiar controls and commands, like `new`, `up`, and `down` commands, but the focus is different: **purpose-built AI learning environments and reusable application blueprints**, not a general-purpose infrastructure-as-code engine.

## Welcome

- **[Getting Started with Instaclustr](https://www.instaclustr.com/support/documentation/getting-started/signing-up/)**: Create an Instaclustr account, explore the managed platform, and [set up an API key](https://www.instaclustr.com/support/documentation/console-features/api-keys/) for provisioning.
- **[Learn with InstAcademy](https://www.instaclustr.com/instacademy-courses/)**: Build your foundation with courses on OpenSearch, RAG, search, and related technologies.
- **[Labs and Workshops](https://github.com/davidvonthenen/labs-mirror)**: Explore the hands-on lab collection. Follow the materials as human-led or self-guided workshops, or use `insta` to turn a supported lab into a managed project.
- **[Docs](https://www.instaclustr.com/support/documentation/)**: Find Instaclustr platform documentation. For CLI commands and options, run `insta --help` or `insta <command> --help`.
- **Community (coming soon)**: A Slack or Discord community link will be added here for questions, examples, and collaboration.

## Getting Started

The following is an example walkthrough that creates a **RAG Retrieval** project, provisions its OpenSearch environment, runs an exercise, and shows how to explore or customize it with Codex.

### 1. Install the CLI

Running the labs requires **Python 3.12**. The CLI creates the project's Python environment and installs its declared dependencies.

To install the latest `insta` release, run the following (see full [installation instructions](TODO) for additional installation options):

```bash
curl -fsSL https://get.instaclustr.com/ | sh
```

For the **optional, but highly recommended** guided exploration through `insta exec`, install either:

- the [Codex CLI](https://developers.openai.com/codex/cli/) and authenticate with `codex login`.
- the [Claude Code CLI](https://claude.com/product/claude-code) and authenticate with `claude login`.

### 2. Find a lab and create a project

```bash
insta lab list
insta new my-first-rag --lab rag-retrieval
cd my-first-rag
```

`insta new` creates a writable lab project, a default `dev` stack, its configuration and task definitions, and a managed Python runtime. You can browse the workshop materials in the [labs repo](https://github.com/davidvonthenen/labs-mirror). 

### 3. Configure your environment

To provision an Instaclustr-managed cluster, provide your **Instaclustr provisioning credentials** through the shell. Your chosen lab may also require model or service API credentials.

```bash
export INSTACLUSTR_PROVISION_USERNAME="<your-Instaclustr-username>"
export INSTACLUSTR_PROVISION_API_KEY="<your-provisioning-API-key>"
export OPENAI_API_KEY="<your-OpenAI-API-key>"
# OR export ANTHROPIC_API_KEY="<your-Claude-API-key>"

insta config
```

The example above uses the RAG Retrieval lab's OpenAI path. Use `ANTHROPIC_API_KEY` for use with the Claude Code CLI.

### 4. Provision your lab infrastructure

```bash
insta up
```

`insta up` prepares the declared environment and, when provisioning is selected, creates the lab's managed OpenSearch cluster. It waits for the cluster to reach its running state, configures the required firewall rules, checks authenticated OpenSearch connectivity, and starts declared support services.

### 5. Run your first AI exercise

For the RAG Retrieval lab, start with the baseline vector-search task:

```bash
insta task list
insta task run baseline-vector-rag ingest
insta task run baseline-vector-rag query
insta task run baseline-vector-rag validate
insta task status baseline-vector-rag
```

The lab defines what each operation runs and which side effects require approval. The baseline query runs the sample questions included in the workshop.

### 6. Go from workshop to blueprint with AI

Start an AI-assisted session inside your lab project:

```bash
# Using `insta` terminal
insta exec --task baseline-vector-rag
```

For example, ask:

> Explain the current RAG pipeline, identify the files responsible for ingestion and retrieval, and propose how to adapt this lab to my organization's documentation. Show the changes and validation plan before implementing them.

Choose the interaction style that fits your workflow:

| Command | Experience |
| --- | --- |
| `insta exec --aprove` | A lightweight conversation loop with eligible declared task effects preapproved. Use only in a trusted lab project. |
| `insta exec -i` | Native interactive Codex or Claude terminal for exploration and coding. |
| `insta exec -i --approve` | Codex or Claude workspace-write/network mode with eligible declared task effects preapproved. Use only in a trusted lab project. |

**Blueprinting is assisted development, not an automatic conversion to production software.** You retain control of the application code, requirements, tests, and decisions. `insta` manages the lab's declared lifecycle and task operations while Codex helps you understand and change the implementation.

### 7. Tear down when you're done

```bash
insta down
```

`insta down` stops CLI-managed services and, after confirmation, removes **infrastructure recorded as owned by the project**. Attached external clusters are not deleted. Your local project files remain available for further learning and iteration.

## Contributing

**Contributor guidelines are coming soon.** We plan to document the development workflow, code conventions, testing expectations, and pull-request process here.

In the meantime, use [GitHub Issues](https://github.com/davidvonthenen/insta-mirror/issues) to propose improvements, describe bugs, or discuss new labs and AI-assisted workflows.

**Start with a lab. Learn how it works. Build what you need.**
