// The program Pulumi runs. Everything it does is in src/: the configuration is read and validated
// once, the stack is built from it, and what the build returns becomes the stack's outputs.
import { load } from "./src/config";
import { deploy } from "./src/stack";

export = deploy(load());
