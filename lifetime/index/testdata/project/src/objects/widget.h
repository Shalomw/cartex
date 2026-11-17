#ifndef DEMO_WIDGET_H_
#define DEMO_WIDGET_H_

#include <memory>
#include <mutex>
#include <vector>

namespace demo {

// Observer receives widget events.
class Observer {
 public:
  void OnEvent();
};

class Widget {
 public:
  Widget();
  ~Widget();

  void RegisterObserver(Observer* observer);
  void UnregisterObserver(Observer* observer);
  void AddListener(std::weak_ptr<Observer> listener);
  void RemoveListener(std::weak_ptr<Observer> listener);

 private:
  std::shared_ptr<Observer> shared_observer_;
  std::weak_ptr<Observer> weak_observer_;
  std::vector<Observer*> observers_;
  Observer* raw_observer_;
  std::mutex mu_;
};

class RefCountedBase {
 public:
  void AddRef();
  void Release();

 private:
  int ref_count_;
};

class Config {
 public:
  static Config& GetInstance();

 private:
  int value_;
};

// 只定义、不分配：验证扫描到 class 定义即生成 family。
class NeverAllocated {
 public:
  void Ping();
};

}  // namespace demo

#endif  // DEMO_WIDGET_H_
