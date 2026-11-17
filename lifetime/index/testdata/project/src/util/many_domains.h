#pragma once

namespace demo {

// 10 个独立线程创建点：与 widget.cc 中的 std::thread/PostTask 一起
// 触发 maxDomains=8 与 maxPairs=8 的截断路径。
inline void SpawnThread1() { std::thread t1([]() {}); }
inline void SpawnThread2() { std::thread t2([]() {}); }
inline void SpawnThread3() { std::thread t3([]() {}); }
inline void SpawnThread4() { std::thread t4([]() {}); }
inline void SpawnThread5() { std::thread t5([]() {}); }
inline void SpawnThread6() { std::thread t6([]() {}); }
inline void SpawnThread7() { std::thread t7([]() {}); }
inline void SpawnThread8() { std::thread t8([]() {}); }
inline void SpawnThread9() { std::thread t9([]() {}); }
inline void SpawnThread10() { std::thread t10([]() {}); }

}  // namespace demo
