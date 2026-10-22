// The contract suite against roomr-fake (ROOMR_BIN_DIR/roomr-fake, else the image).
const { describeContract, fakeTarget } = require("../dist/contract");

describe("against roomr-fake", function () {
  describeContract(() => fakeTarget());
});
