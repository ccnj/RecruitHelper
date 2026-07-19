import {
  Kind,
  validateKindBody,
  validatePrimitiveData,
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
