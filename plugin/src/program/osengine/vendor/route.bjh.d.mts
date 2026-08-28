// 我们写的类型声明,见 ../README.md。
//
// 刻意声明成 unknown:池子的内部结构(每条 `[D, 起始驻留, 中途停顿[], 末次瞄准]`)
// 只有上游的 `route.mjs` 解释,我们从头到尾只做转交。抄一份结构进来,等于在
// 我们这边复制一份会过期的知识。
export declare const ROUTES: unknown
