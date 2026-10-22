// The contract suite against the real node binary (ROOMR_BIN_DIR: roomr and sipcaller).
const { describeContract, realTarget } = require("../dist/contract");

describe("against roomr", function () {
  describeContract(() => realTarget());
});
