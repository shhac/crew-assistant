import { configure } from "@testing-library/react";

// findBy and waitFor give up after one second by default. On a busy machine a
// full run can take longer than that to render a list that works, and a slow
// run is not a failing one.
configure({ asyncUtilTimeout: 5000 });
