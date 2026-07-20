import {
  Kind,
  validateKindBody,
  validatePrimitiveData,
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
