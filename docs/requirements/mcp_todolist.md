# MCP TODO List Management

### Core Intent

- Provide **MCP tools** that allow an Agent (acting as a client) to manage a **TODO list**.
- Each Agent is tied to an **API key**, which acts as the unique identifier for a user.
- **Data isolation**: TODO lists must be scoped per API key, ensuring no cross-user data leakage.

### Functional Requirements

- **CRUD operations** on tasks:
  - Add, delete, view, update (mark done/undone).
- **Task state management**:
  - Support toggling between _completed_ and _not completed_.
  - Ability to clear all completed tasks in bulk.
- **Retrieval flexibility**:
  - Option to filter tasks by completion status.
- **Frontend console**:
  - GUI for non-programmatic interaction.
  - Must support all CRUD operations visually.

### Observations

- Current design is **function-oriented**, but lacks:
  - **Task metadata** (e.g., IDs, timestamps, priority).
  - **Error handling** (e.g., what if a task doesn’t exist?).
  - **Scalability considerations** (pagination, large lists).
  - **Consistency guarantees** (atomic operations, concurrency).
- The frontend requirement is underspecified (console could mean CLI or web dashboard).

## User Story

**As an authenticated Agent (identified by API key),
I want to manage my personal TODO list through MCP tools and a console interface,
so that I can reliably add, update, delete, and track tasks in isolation from other users.**

### Acceptance Criteria

1. **User Isolation**
   - Each API key maps to a distinct TODO list namespace.
   - No data leakage between users.
2. **Task Management**
   - I can add one or multiple tasks at once.
   - I can mark tasks as completed or revert them to undone.
   - I can delete tasks individually or in bulk.
   - I can clear all completed tasks in one operation.
3. **Task Retrieval**
   - I can view my TODO list with an option to include/exclude completed tasks.
   - Tasks are returned with metadata (ID, description, status, timestamp).
4. **Frontend Console**
   - I can interact with my TODO list via a graphical console.
   - The console supports add, delete, mark done/undone, and view operations.
   - The console reflects real-time state changes.

## Request

### MCP Toolset

- **Functions**
  - `add_todos(tasks []string) → []Task`
    - Adds tasks, returns created task objects with IDs.
  - `mark_todo_done(task_ids []string)`
    - Marks tasks as completed by ID.
  - `mark_todo_undone(task_ids []string)`
    - Reverts tasks to undone by ID.
  - `delete_todos(task_ids []string)`
    - Deletes tasks by ID.
  - `get_todos(show_completed bool, limit int, offset int) → []Task`
    - Retrieves tasks with pagination and optional filtering.
  - `clear_completed_todos()`
    - Bulk clears completed tasks.

### Frontend Console

- **Features**
  - Web-based dashboard (preferred over CLI for usability).
  - CRUD operations mapped to buttons and forms.
  - Task list view with filters (all, pending, completed).
  - Bulk actions (clear completed, delete multiple).
  - Responsive design for desktop and mobile.

### Non-Functional Requirements

- **Security**: API key authentication, strict isolation.
- **Scalability**: Support large lists with pagination.
- **Reliability**: Atomic operations, error handling (e.g., invalid task IDs).
- **Usability**: Intuitive console UI with real-time updates.
