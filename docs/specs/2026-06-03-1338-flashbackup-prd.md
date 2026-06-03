---
title: FlashBackup PRD
created: 2026-06-03
last_modified: 2026-06-03
author: Mahesh Mirchandani
status: source-spec
supersedes: ../../Portable macOS Backup Utility.txt
---

# 🧰 Portable macOS Backup Utility

## Full PRD + Technical Specification + Execution Prompt

---

# 1. 📌 Product Overview

## 1.1 Product Name (Working)

**FlashBackup (working title)**

## 1.2 Objective

Build a **portable, professional-grade backup utility for macOS** that:

* Runs directly from a **USB flash drive (no installation)**
* Supports **copy and move operations**
* Ensures **data integrity via validation**
* Provides **flexible file and directory selection**

---

# 2. 🎯 Goals & Non-Goals

## Goals

* Safe and reliable file transfer
* True portability (USB runnable)
* Multi-directory and filtered backups
* Strong validation (hash-based)
* Reuse of proven open-source tools where possible

## Non-Goals (MVP)

* Cloud backup integration
* Real-time sync
* macOS Time Machine replacement
* Network-based backups

---

# 3. 👤 Target Users

### Primary

* Power users (developers, analysts, consultants)
* Users needing **portable backup tools across machines**

### Secondary

* Semi-technical users needing simple UI

---

# 4. 🧩 Core Features

## 4.1 Drive Discovery

* Auto-detect:

  * Internal drives
  * External USB drives
* Display:

  * Name
  * Mount path
  * Available space

---

## 4.2 Source & Destination Selection

* Select:

  * Multiple directories
  * Individual folders/files
* Allow:

  * Cross-drive copy
  * Same-disk copy

---

## 4.3 File Filtering

* Include filters:

  * Extensions (.pdf, .jpg, etc.)
* Exclude filters:

  * Patterns (e.g., *.tmp, .DS_Store)
* Optional presets:

  * Documents
  * Media
  * Code

---

## 4.4 Copy / Move Engine

### Modes:

* Copy (default)
* Move:

  * Delete source ONLY after validation success

### Requirements:

* Handle large files (>10GB)
* Resume interrupted transfers (preferred)
* Preserve:

  * Permissions
  * Timestamps

---

## 4.5 Validation System (Critical)

### Levels:

1. Basic:

   * File size match
2. Standard:

   * Size + timestamp
3. Advanced (default):

   * SHA256 hash comparison

### Output:

* Validation report:

  * Passed files
  * Failed files
  * Missing files

### Rules:

* Move operation requires **successful validation**
* Failures must block deletion

---

## 4.6 Safety Features

* Dry-run mode (no changes)
* Overwrite confirmation
* Disk space validation before start
* Interrupt-safe operations
* Logging for all actions

---

## 4.7 Logging & Reporting

* Log file stored on:

  * USB drive (default)
* Includes:

  * Timestamp
  * Operation type
  * Errors
  * Validation results

---

## 4.8 UX / Interface

### Option A (Preferred for MVP):

CLI tool with:

* Menu-driven interaction
* Clear prompts

### Option B:

Lightweight GUI:

* Native (SwiftUI) OR
* Local web UI

---

# 5. ⚙️ Technical Architecture

## 5.1 Key Requirement: Portability

* Must run from USB without installation
* No admin privileges required

---

## 5.2 Recommended Tech Stack (to evaluate)

### Option 1: Go (Preferred)

* Pros:

  * Single static binary
  * Fast
  * Cross-platform
* Cons:

  * Basic UI

---

### Option 2: Rust

* Pros:

  * High performance
  * Memory safety
* Cons:

  * Slower development

---

### Option 3: Swift (macOS native)

* Pros:

  * Best UI
* Cons:

  * Harder portability

---

## 5.3 Transfer Engine Strategy

### MUST evaluate open-source:

* rsync
* rclone
* restic (if relevant)

### Decision Criteria:

* License (MIT, Apache, BSD only)
* Stability
* Performance

### Likely Direction:

Wrap **rsync** for:

* Speed
* Reliability
* Resume support

---

## 5.4 System Modules

### 1. Drive Scanner

* Detect mounted volumes

### 2. Selection Engine

* File/directory selection
* Filter application

### 3. Transfer Engine

* Copy/move logic
* Resume support

### 4. Validation Engine

* Hash computation (SHA256)
* Comparison

### 5. Logging Module

* Structured logs

### 6. CLI/UX Layer

* User interaction

---

# 6. 🔄 Execution Flow

1. Scan drives
2. User selects:

   * Source
   * Destination
3. Configure:

   * Filters
   * Mode (copy/move)
   * Validation level
4. Dry run (optional)
5. Execute transfer
6. Run validation
7. Generate report
8. If move:

   * Delete source only if validation passes

---

# 7. ⚠️ Edge Cases

* Interrupted transfer
* Disk full mid-operation
* File locked/in-use
* Permission denied
* Duplicate filenames
* Symlinks

---

# 8. 📦 Packaging Requirements

* Output:

  * Single executable OR bundled app
* Must:

  * Run from USB
  * Store config/logs locally

---

# 9. 🧪 Testing Requirements

* Test cases:

  * Large files
  * Mixed file types
  * Failure scenarios
* Validation accuracy tests
* Cross-device testing

---

# 10. 🚀 Claude Execution Instructions

You must follow this EXACT sequence:

---

## STEP 1: Clarification

Ask detailed questions about:

* CLI vs GUI preference
* Validation strictness
* Target user simplicity vs power

DO NOT skip.

---

## STEP 2: Open Source Research

Search GitHub and present:

* 3–5 candidate tools (rsync, rclone, etc.)
* License, pros/cons
* Recommendation

---

## STEP 3: Architecture Proposal

Provide:

* 2–3 architecture options
* Final recommendation with justification

---

## STEP 4: Feature Finalization

Work with me to:

* Lock MVP scope
* Define advanced features

---

## STEP 5: Implementation Plan

Provide:

* Folder structure
* Modules
* Dependencies
* Build process

---

## STEP 6: Development

Generate:

* Production-quality code
* Error handling
* Logging
* Validation

---

## STEP 7: Packaging

Explain:

* How to compile
* How to run from USB
* macOS compatibility (Intel + Apple Silicon)

---

# 11. 🛑 Critical Rules

* DO NOT start coding without approval
* PRIORITIZE data safety over speed
* DO NOT implement unsafe move logic
* ALWAYS include validation before deletion
* USE existing tools where possible

---

# 12. ✅ Output Expectations

* Be structured
* Use tables where helpful
* Clearly separate:

  * Assumptions
  * Recommendations
  * Questions

---

## Start now with:

1. Clarifying questions
2. Then proceed to GitHub research

DO NOT skip steps.
