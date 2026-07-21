import {
  Kind,
  PRIMITIVE_META,
  SendSurfaceDiagnosticStage,
  validateKindBody,
  validatePrimitiveData,
  validatePrimitiveGuards,
  validatePrimitiveResult,
  validateSchema,
  type ValidationIssue,
} from "./protocol.gen";

function expectValid(label: string, issues: readonly ValidationIssue[]): void {
  if (issues.length > 0) throw new Error(`${label}: ${JSON.stringify(issues)}`);
}

function expectIssue(label: string, issues: readonly ValidationIssue[], path: string, rule: string): void {
  if (!issues.some((issue) => issue.path === path && issue.rule === rule)) {
    throw new Error(`${label}: missing ${path}/${rule}: ${JSON.stringify(issues)}`);
  }
}

const validCommand = {
  name: "chat.readList",
  ver: 1,
  context: {
    platform: "zhilian",
    accountRef: "acc-01",
    expectedPrincipalFingerprint: "opaque-01",
    futureContextField: true,
  },
  args: { filter: "all", maxSessions: 32, futureArgField: true },
  deadline: 1_999_999_999_999,
  execBudgetMs: 240_000,
  leaseMs: 60_000,
  futureBodyField: true,
};
expectValid("must-ignore", validateKindBody(Kind.Cmd, validCommand));
const validLocalHello = {
  handId: "h-local-random",
  bootId: "b-one",
  protoSupported: [1],
  contractHash: "sha256:public-digest",
  app: { extVersion: "0.1.0", browser: "chrome" },
  caps: [],
  features: [],
  auth: "retired-and-ignored",
};
expectValid("local hello and retired field must-ignore", validateKindBody(Kind.Hello, validLocalHello));
const helloWithoutHandID: Record<string, unknown> = { ...validLocalHello };
delete helloWithoutHandID.handId;
expectIssue("local handId required", validateKindBody(Kind.Hello, helloWithoutHandID), "$.handId", "required");
expectIssue(
  "local handId non-null",
  validateKindBody(Kind.Hello, { ...validLocalHello, handId: null }),
  "$.handId",
  "nullable",
);
expectValid(
  "pre-bind probe without context",
  validateKindBody(Kind.Cmd, {
    name: "probe.platform",
    ver: 1,
    args: {},
    deadline: 1_999_999_999_999,
    execBudgetMs: 5_000,
  }),
);
expectValid(
  "debug.reload empty args through command channel",
  validateKindBody(Kind.Cmd, {
    name: "debug.reload",
    ver: 1,
    args: {},
    deadline: 1_999_999_999_999,
    execBudgetMs: 5_000,
  }),
);
expectValid(
  "debug.reload empty result data",
  validatePrimitiveResult("debug.reload", 1, {
    ref: "reload-1",
    status: "ok",
    data: {},
    replayed: false,
    execMs: 1,
  }),
);
const commandWithoutContext: Record<string, unknown> = { ...validCommand };
delete commandWithoutContext.context;
expectIssue(
  "non-probe context required",
  validateKindBody(Kind.Cmd, commandWithoutContext),
  "$.context",
  "required",
);
expectIssue(
  "args enum",
  validateKindBody(Kind.Cmd, { ...validCommand, args: { filter: "bad" } }),
  "$.args.filter",
  "enum",
);
expectIssue(
  "fingerprint",
  validateKindBody(Kind.Cmd, { ...validCommand, context: { platform: "zhilian", accountRef: "acc-01" } }),
  "$.context.expectedPrincipalFingerprint",
  "required",
);

expectValid(
  "list continuation",
  validatePrimitiveData("chat.readList", 1, { sessions: [], complete: false, nextCursor: "opaque-next" }),
);
expectIssue(
  "list cursor required",
  validatePrimitiveData("chat.readList", 1, { sessions: [], complete: false }),
  "$.nextCursor",
  "requiredWhen",
);
expectValid(
  "thread continuation",
  validatePrimitiveData("chat.readThread", 1, {
    messages: [],
    reachedTop: false,
    anchorMatched: false,
    peer: null,
    complete: false,
    nextCursor: "opaque-next",
  }),
);
expectValid(
  "thread sourceKey",
  validatePrimitiveData("chat.readThread", 1, {
    messages: [{
      idx: 0,
      direction: "in",
      kind: "text",
      text: "拒绝模板",
      blobRef: null,
      contentHash: "hash",
      sourceKey: "a".repeat(64),
    }],
    reachedTop: false,
    anchorMatched: true,
    peer: null,
    complete: true,
  }),
);
expectIssue(
  "thread sourceKey exact length",
  validatePrimitiveData("chat.readThread", 1, {
    messages: [{
      idx: 0,
      direction: "in",
      kind: "text",
      text: "拒绝模板",
      blobRef: null,
      contentHash: "hash",
      sourceKey: "a".repeat(63),
    }],
    reachedTop: false,
    anchorMatched: true,
    peer: null,
    complete: true,
  }),
  "$.messages[0].sourceKey",
  "minLength",
);
expectIssue(
  "thread completion invariant",
  validatePrimitiveData("chat.readThread", 1, {
    messages: [],
    reachedTop: false,
    anchorMatched: false,
    peer: null,
    complete: true,
  }),
  "$",
  "atLeastOneTrueWhen",
);

const event = {
  name: "unreadBadge",
  context: { platform: "zhilian", accountRef: "acc-01" },
  observedAt: 1,
  data: { scope: "total", value: 2, prev: 1, stable: true, futureEventField: true },
};
expectValid("event", validateKindBody(Kind.Event, event));
expectIssue(
  "event const",
  validateKindBody(Kind.Event, { ...event, data: { ...event.data, stable: false } }),
  "$.data.stable",
  "const",
);
expectIssue("safe integer", validateKindBody(Kind.Pong, { now: 9_007_199_254_740_992 }), "$.now", "safeInteger");

const witnessHello = {
  ...validLocalHello,
  caps: ["chat.sendMessage@1"],
  features: ["witness/1"],
  witnessStoreId: "w-1",
  outboxPending: 0,
  journalOpen: 0,
};
expectValid("witness hello", validateKindBody(Kind.Hello, witnessHello));
expectIssue(
  "witness hello fields required",
  validateKindBody(Kind.Hello, { ...validLocalHello, features: ["witness/1"] }),
  "$",
  "witnessAdvertisement",
);

const sendCommand = {
  name: "chat.sendMessage",
  ver: 1,
  context: { platform: "zhilian", accountRef: "acc-1", expectedPrincipalFingerprint: "opaque" },
  args: { conversationRef: "conv-1", text: "你好" },
  idemKey: "ik1:zhilian:acc-1:chat.sendMessage:conv-1:int-1",
  deadline: 1_999_999_999_999,
  execBudgetMs: 60_000,
  leaseMs: 30_000,
  guards: { expectedTail: [{ direction: "in", contentHash: "abc" }] },
};
expectValid("send command", validateKindBody(Kind.Cmd, sendCommand));
const sendWithoutGuards: Record<string, unknown> = { ...sendCommand };
delete sendWithoutGuards.guards;
expectIssue("send guards", validateKindBody(Kind.Cmd, sendWithoutGuards), "$.guards", "required");

const committedJournal = {
  ref: "cmd-1",
  idemKey: "ik-1",
  state: "committed",
  startedAt: 10,
  committedAt: 20,
  result: {
    ref: "cmd-1",
    status: "ok",
    data: { conversationRef: "conv-1", contentHash: "a".repeat(64), observedAt: 20 },
    evidence: [{ type: "outboundMessageObserved" }],
    replayed: false,
    execMs: 10,
  },
  expiresAt: 100,
};
expectValid("committed journal", validateSchema("JournalEntry", committedJournal));
expectIssue(
  "committed journal null result",
  validateSchema("JournalEntry", { ...committedJournal, result: null }),
  "$.result",
  "nullable",
);
expectIssue(
  "attempting journal forbids result",
  validateSchema("JournalEntry", { ...committedJournal, state: "attempting", committedAt: undefined }),
  "$.result",
  "forbiddenWhen",
);

const outboxEntry = {
  message: {
    proto: 1,
    kind: "result",
    msgId: "result-1",
    session: "session-created",
    ts: 20,
    attempt: 1,
    body: { ref: "cmd-1", status: "expired", replayed: false, execMs: 0 },
  },
  createdAt: 20,
  expiresAt: 100,
};
expectValid("outbox", validateSchema("OutboxEntry", outboxEntry));
expectIssue(
  "outbox session non-null",
  validateSchema("OutboxEntry", { ...outboxEntry, message: { ...outboxEntry.message, session: null } }),
  "$.message.session",
  "nullable",
);

const doneReport = {
  ref: "cmd-1",
  witnessStoreId: "w-1",
  state: "done",
  result: committedJournal.result,
  journal: {
    ref: "cmd-1",
    idemKey: "ik-1",
    state: "committed",
    startedAt: 10,
    committedAt: 20,
  },
};
expectValid("done report", validateKindBody(Kind.Report, doneReport));
expectIssue(
  "done report null",
  validateKindBody(Kind.Report, { ...doneReport, result: null, journal: null }),
  "$.result",
  "requiredWhen",
);
expectIssue(
  "attempting report state",
  validateKindBody(Kind.Report, { ...doneReport, state: "attempting", result: null }),
  "$.journal.state",
  "state",
);

const sendResult = committedJournal.result;
expectValid("send result", validatePrimitiveResult("chat.sendMessage", 1, sendResult));
expectIssue(
  "send result evidence required",
  validatePrimitiveResult("chat.sendMessage", 1, { ...sendResult, evidence: [] }),
  "$.evidence",
  "minItems",
);
expectIssue(
  "send result evidence enum",
  validatePrimitiveResult("chat.sendMessage", 1, { ...sendResult, evidence: [{ type: "clicked" }] }),
  "$.evidence[0].type",
  "enum",
);

for (const contactState of ["unestablished", "established", "unknown"] as const) {
  expectValid(
    `candidate current state ${contactState}`,
    validatePrimitiveData("candidate.readCurrent", 1, {
      platformUserRef: "user-1",
      displayName: null,
      positionRef: "job-1",
      positionTitle: null,
      contactState,
    }),
  );
}
expectValid(
  "candidate current command",
  validateKindBody(Kind.Cmd, {
    name: "candidate.readCurrent",
    ver: 1,
    context: { platform: "zhilian", accountRef: "acc-1", expectedPrincipalFingerprint: "opaque" },
    args: {},
    deadline: 1_999_999_999_999,
    execBudgetMs: 5_000,
  }),
);
expectIssue(
  "candidate current identity required",
  validatePrimitiveData("candidate.readCurrent", 1, {
    displayName: null,
    positionRef: "job-1",
    positionTitle: null,
    contactState: "unknown",
  }),
  "$.platformUserRef",
  "required",
);
expectIssue(
  "candidate display name nullable but required",
  validatePrimitiveData("candidate.readCurrent", 1, {
    platformUserRef: "user-1",
    positionRef: "job-1",
    positionTitle: null,
    contactState: "unknown",
  }),
  "$.displayName",
  "required",
);
expectIssue(
  "candidate current state enum",
  validatePrimitiveData("candidate.readCurrent", 1, {
    platformUserRef: "user-1",
    displayName: null,
    positionRef: "job-1",
    positionTitle: null,
    contactState: "bad",
  }),
  "$.contactState",
  "enum",
);

const resumeCommand = {
  name: "candidate.readResume",
  ver: 1,
  context: { platform: "zhilian", accountRef: "acc-1", expectedPrincipalFingerprint: "opaque" },
  args: { conversationRef: "conv-1", platformUserRef: "user-1" },
  deadline: 1_999_999_999_999,
  execBudgetMs: 60_000,
  leaseMs: 30_000,
};
expectValid("candidate resume command", validateKindBody(Kind.Cmd, resumeCommand));
const resumeWithoutLease: Record<string, unknown> = { ...resumeCommand };
delete resumeWithoutLease.leaseMs;
expectIssue("candidate resume lease required", validateKindBody(Kind.Cmd, resumeWithoutLease), "$.leaseMs", "required");
expectIssue(
  "candidate resume idem forbidden",
  validateKindBody(Kind.Cmd, { ...resumeCommand, idemKey: "forbidden" }),
  "$.idemKey",
  "forbidden",
);
expectIssue(
  "candidate resume guards forbidden",
  validateKindBody(Kind.Cmd, { ...resumeCommand, guards: {} }),
  "$.guards",
  "forbidden",
);

const resumeMeta = PRIMITIVE_META["candidate.readResume"];
const expectedResumePreconditions = [
  "context.platform",
  "context.accountRef",
  "context.expectedPrincipalFingerprint",
  "surface.im",
  "login.in",
  "conversation.tracked",
  "manualQuiet",
];
if (
  resumeMeta.ver !== 1 || resumeMeta.class !== "intrusive" || resumeMeta.batch !== "X" ||
  resumeMeta.platformSideEffect !== "none" || resumeMeta.execBudgetMs !== 60_000 ||
  resumeMeta.deadlineMs !== 120_000 || resumeMeta.leaseMs !== 30_000 ||
  resumeMeta.guardsSchema !== null || resumeMeta.evidenceSchema !== null ||
  resumeMeta.verificationPrimitive !== null ||
  JSON.stringify(resumeMeta.preconditions) !== JSON.stringify(expectedResumePreconditions)
) {
  throw new Error(`candidate.readResume metadata drift: ${JSON.stringify(resumeMeta)}`);
}

const validResumeData: Record<string, unknown> = {
  conversationRef: "conv-1",
  platformUserRef: "user-1",
  observedAt: 20,
  basic: [{ label: "姓名", value: "" }],
  expectations: [],
  selfEvaluation: "",
  education: "本科",
  workExperiences: "",
};
expectValid("candidate resume data", validatePrimitiveData("candidate.readResume", 1, validResumeData));
for (const field of [
  "conversationRef", "platformUserRef", "observedAt", "basic", "expectations",
  "selfEvaluation", "education", "workExperiences",
]) {
  const missing = { ...validResumeData };
  delete missing[field];
  expectIssue(
    `candidate resume ${field} required`,
    validatePrimitiveData("candidate.readResume", 1, missing),
    `$.${field}`,
    "required",
  );
}
expectIssue(
  "candidate resume observedAt minimum",
  validatePrimitiveData("candidate.readResume", 1, { ...validResumeData, observedAt: -1 }),
  "$.observedAt",
  "minimum",
);
expectIssue(
  "candidate resume observedAt int64",
  validatePrimitiveData("candidate.readResume", 1, { ...validResumeData, observedAt: "2026-07-21T00:00:00Z" }),
  "$.observedAt",
  "type",
);
expectIssue(
  "candidate resume label non-empty",
  validatePrimitiveData("candidate.readResume", 1, { ...validResumeData, basic: [{ label: "", value: "" }] }),
  "$.basic[0].label",
  "minLength",
);
expectIssue(
  "candidate resume section non-null",
  validatePrimitiveData("candidate.readResume", 1, { ...validResumeData, basic: null }),
  "$.basic",
  "nullable",
);

const resumeBoundary: Record<string, unknown> = {
  conversationRef: "conv-1",
  platformUserRef: "user-1",
  observedAt: 20,
  basic: [],
  expectations: [],
  selfEvaluation: "",
  education: "",
  workExperiences: "",
};
const resumeBaseBytes = new TextEncoder().encode(JSON.stringify(resumeBoundary)).length;
resumeBoundary.selfEvaluation = "a".repeat(65_536 - resumeBaseBytes);
const resumeLimitBytes = new TextEncoder().encode(JSON.stringify(resumeBoundary)).length;
if (resumeLimitBytes !== 65_536) throw new Error(`resume boundary fixture bytes=${resumeLimitBytes}`);
expectValid("candidate resume 65536 bytes", validatePrimitiveData("candidate.readResume", 1, resumeBoundary));
expectIssue(
  "candidate resume 65537 bytes",
  validatePrimitiveData("candidate.readResume", 1, { ...resumeBoundary, selfEvaluation: `${resumeBoundary.selfEvaluation}a` }),
  "$",
  "maxJsonBytes",
);

const greetingCommand = {
  name: "chat.sendGreeting",
  ver: 1,
  context: { platform: "zhilian", accountRef: "acc-1", expectedPrincipalFingerprint: "opaque" },
  args: { platformUserRef: "user-1", positionRef: "job-1", text: "你好" },
  idemKey: "ik1:zhilian:acc-1:chat.sendGreeting:profile-1:int-1",
  deadline: 1_999_999_999_999,
  execBudgetMs: 60_000,
  leaseMs: 30_000,
  guards: { expectUnestablished: true },
};
expectValid("greeting command", validateKindBody(Kind.Cmd, greetingCommand));
const greetingWithoutGuards: Record<string, unknown> = { ...greetingCommand };
delete greetingWithoutGuards.guards;
expectIssue("greeting guards required", validateKindBody(Kind.Cmd, greetingWithoutGuards), "$.guards", "required");
expectIssue(
  "greeting text non-empty",
  validateKindBody(Kind.Cmd, { ...greetingCommand, args: { ...greetingCommand.args, text: "" } }),
  "$.args.text",
  "minLength",
);
expectIssue(
  "greeting text UTF-8 byte limit",
  validateKindBody(Kind.Cmd, { ...greetingCommand, args: { ...greetingCommand.args, text: "界".repeat(683) } }),
  "$.args.text",
  "maxBytes",
);
expectValid("greeting guard", validatePrimitiveGuards("chat.sendGreeting", 1, { expectUnestablished: true }));
expectIssue(
  "greeting guard const",
  validatePrimitiveGuards("chat.sendGreeting", 1, { expectUnestablished: false }),
  "$.expectUnestablished",
  "const",
);

const greetingResult = {
  ref: "greet-1",
  status: "ok",
  data: {
    platformUserRef: "user-1",
    positionRef: "job-1",
    conversationRef: "conv-1",
    contentHash: "a".repeat(64),
    observedAt: 20,
  },
  evidence: [{ type: "outboundGreetingObserved" }],
  replayed: false,
  execMs: 10,
};
expectValid("greeting result", validatePrimitiveResult("chat.sendGreeting", 1, greetingResult));
expectIssue(
  "greeting evidence required",
  validatePrimitiveResult("chat.sendGreeting", 1, { ...greetingResult, evidence: [] }),
  "$.evidence",
  "minItems",
);
expectIssue(
  "greeting evidence enum",
  validatePrimitiveResult("chat.sendGreeting", 1, { ...greetingResult, evidence: [{ type: "outboundMessageObserved" }] }),
  "$.evidence[0].type",
  "enum",
);
expectIssue(
  "greeting hash length",
  validatePrimitiveResult("chat.sendGreeting", 1, {
    ...greetingResult,
    data: { ...greetingResult.data, contentHash: "a".repeat(63) },
  }),
  "$.data.contentHash",
  "minLength",
);
expectIssue(
  "greeting hash maximum length",
  validatePrimitiveResult("chat.sendGreeting", 1, {
    ...greetingResult,
    data: { ...greetingResult.data, contentHash: "a".repeat(65) },
  }),
  "$.data.contentHash",
  "maxLength",
);

expectValid(
  "greeting outcome command",
  validateKindBody(Kind.Cmd, {
    name: "chat.readGreetingOutcome",
    ver: 1,
    context: { platform: "zhilian", accountRef: "acc-1", expectedPrincipalFingerprint: "opaque" },
    args: { platformUserRef: "user-1", positionRef: "job-1", contentHash: "a".repeat(64) },
    deadline: 1_999_999_999_999,
    execBudgetMs: 240_000,
    leaseMs: 30_000,
  }),
);
expectValid(
  "confirmed greeting outcome",
  validatePrimitiveData("chat.readGreetingOutcome", 1, {
    confirmed: true,
    conversationRef: "conv-1",
    contentHash: "a".repeat(64),
    observedAt: 20,
  }),
);
expectIssue(
  "confirmed greeting outcome needs conversation",
  validatePrimitiveData("chat.readGreetingOutcome", 1, {
    confirmed: true,
    contentHash: "a".repeat(64),
    observedAt: 20,
  }),
  "$.conversationRef",
  "requiredWhen",
);
expectIssue(
  "confirmed greeting outcome needs hash",
  validatePrimitiveData("chat.readGreetingOutcome", 1, {
    confirmed: true,
    conversationRef: "conv-1",
    observedAt: 20,
  }),
  "$.contentHash",
  "requiredWhen",
);
expectValid(
  "unconfirmed greeting outcome",
  validatePrimitiveData("chat.readGreetingOutcome", 1, { confirmed: false, observedAt: 20 }),
);
expectIssue(
  "unconfirmed greeting outcome forbids conversation",
  validatePrimitiveData("chat.readGreetingOutcome", 1, {
    confirmed: false,
    conversationRef: "conv-1",
    observedAt: 20,
  }),
  "$.conversationRef",
  "forbiddenWhen",
);
expectIssue(
  "unconfirmed greeting outcome forbids hash",
  validatePrimitiveData("chat.readGreetingOutcome", 1, {
    confirmed: false,
    contentHash: "a".repeat(64),
    observedAt: 20,
  }),
  "$.contentHash",
  "forbiddenWhen",
);

const greetingRejected = {
  ref: "greet-1",
  status: "failed",
  error: { code: "GREETING_REJECTED", retryable: "no", sideEffect: "none" },
  replayed: false,
  execMs: 1,
};
expectValid("greeting rejected none", validatePrimitiveResult("chat.sendGreeting", 1, greetingRejected));
expectIssue(
  "greeting rejected requires side effect",
  validatePrimitiveResult("chat.sendGreeting", 1, {
    ...greetingRejected,
    error: { code: "GREETING_REJECTED", retryable: "no" },
  }),
  "$.error.sideEffect",
  "required",
);
expectIssue(
  "greeting rejected forbids possible",
  validatePrimitiveResult("chat.sendGreeting", 1, {
    ...greetingRejected,
    error: { ...greetingRejected.error, sideEffect: "possible" },
  }),
  "$.error.sideEffect",
  "errorCodeSideEffect",
);
expectIssue(
  "greeting rejected forbids confirmed",
  validatePrimitiveResult("chat.sendGreeting", 1, {
    ...greetingRejected,
    error: { ...greetingRejected.error, sideEffect: "confirmed" },
  }),
  "$.error.sideEffect",
  "errorCodeSideEffect",
);

if (
  PRIMITIVE_META["chat.sendGreeting"].verificationPrimitive !== "chat.readGreetingOutcome" ||
  PRIMITIVE_META["chat.sendGreeting"].verificationVer !== 1 ||
  PRIMITIVE_META["chat.sendGreeting"].verificationMaxRounds !== 3
) {
  throw new Error(`greeting verifier metadata drift: ${JSON.stringify(PRIMITIVE_META["chat.sendGreeting"])}`);
}
const greetingMeta = PRIMITIVE_META["chat.sendGreeting"];
if (greetingMeta.ver !== 1 || greetingMeta.guardsSchema === null || greetingMeta.evidenceSchema === null) {
  throw new Error(`greeting schema metadata drift: ${JSON.stringify(greetingMeta)}`);
}
const inviteMeta = PRIMITIVE_META["chat.sendInviteCard"];
if (
  inviteMeta.ver !== 0 ||
  inviteMeta.argsSchema !== null ||
  inviteMeta.dataSchema !== null ||
  inviteMeta.guardsSchema !== null ||
  inviteMeta.evidenceSchema !== null
) {
  throw new Error(`sendInviteCard placeholder drift: ${JSON.stringify(inviteMeta)}`);
}
expectIssue(
  "sendInviteCard placeholder has no schema",
  validateKindBody(Kind.Cmd, {
    name: "chat.sendInviteCard",
    ver: 1,
    context: { platform: "zhilian", accountRef: "acc-1", expectedPrincipalFingerprint: "opaque" },
    args: {},
    idemKey: "ik-1",
    deadline: 1_999_999_999_999,
    execBudgetMs: 60_000,
  }),
  "$.name",
  "primitive",
);

const retainedSendSurfaceStages = [
  "page_absent",
  "route_missing",
  "composer_cardinality",
  "detail_cardinality",
  "button_cardinality",
  "dom_containment",
  "button_form_unsafe",
  "draft_present",
  "thread_unavailable",
  "diagnostic_unavailable",
  "ready",
] as const;
const generatedSendSurfaceStages = Object.values(SendSurfaceDiagnosticStage);
if (JSON.stringify(generatedSendSurfaceStages) !== JSON.stringify(retainedSendSurfaceStages)) {
  throw new Error(`send-surface stages drift: ${JSON.stringify(generatedSendSurfaceStages)}`);
}
for (const stage of retainedSendSurfaceStages) {
  expectValid(
    `current send-surface stage ${stage}`,
    validatePrimitiveResult("debug.inspectSendSurface", 1, {
      ref: "debug-surface-1",
      status: "ok",
      data: { ready: stage === "ready", stage },
      replayed: false,
      execMs: 1,
    }),
  );
}
expectIssue(
  "removed private send-surface stage",
  validatePrimitiveResult("debug.inspectSendSurface", 1, {
    ref: "debug-surface-1",
    status: "ok",
    data: { ready: false, stage: "component_tree_unavailable" },
    replayed: false,
    execMs: 1,
  }),
  "$.data.stage",
  "enum",
);

expectValid(
  "witness unavailable before action",
  validatePrimitiveResult("chat.sendMessage", 1, {
    ref: "cmd-1",
    status: "failed",
    error: {
      code: "WITNESS_UNAVAILABLE",
      retryable: "afterRecovery",
      sideEffect: "none",
      data: { reason: "writeFailed" },
    },
    replayed: false,
    execMs: 1,
  }),
);
expectIssue(
  "witness unavailable data required",
  validatePrimitiveResult("chat.sendMessage", 1, {
    ref: "cmd-1",
    status: "failed",
    error: { code: "WITNESS_UNAVAILABLE", retryable: "afterRecovery", sideEffect: "none" },
    replayed: false,
    execMs: 1,
  }),
  "$.error.data",
  "required",
);
