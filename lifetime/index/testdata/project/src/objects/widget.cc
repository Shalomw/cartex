#include "src/objects/widget.h"

#include <thread>

namespace demo {

Widget::Widget() : shared_observer_(std::make_shared<Observer>()) {}

Widget::~Widget() {
  delete raw_observer_;
}

void Widget::RegisterObserver(Observer* observer) {
  std::lock_guard<std::mutex> guard(mu_);
  observers_.push_back(observer);
}

void Widget::UnregisterObserver(Observer* observer) {
  std::lock_guard<std::mutex> guard(mu_);
  observers_.erase(observers_.begin(), observers_.end(), observer);
}

void Widget::AddListener(std::weak_ptr<Observer> listener) { (void)listener; }

void Widget::RemoveListener(std::weak_ptr<Observer> listener) { (void)listener; }

Widget* CreateWidget() {
  Widget* widget = new Widget();
  std::thread worker([]() {});
  worker.detach();
  return widget;
}

RefCountedBase* CreateRefCounted() { return new RefCountedBase(); }

Config& Config::GetInstance() {
  static Config instance;
  return instance;
}

void UseFactories() {
  Registry::GetInstance();
  Singleton<Registry>::Instance();
  ObjectPool<Observer> pool;
  HandleTable<Observer> table;
  PostTask([]() {});
}

// JSNumberFormat 的类定义不在被索引文件里，只有方法实现。
// 索引器必须仍以 Name::Method 定义生成 family。
void JSNumberFormat::Format() {
  // 没有 new，也没有类定义被索引到。
}

// 与 coord 要求一致的 Foo::Bar 方法定义占位：无 new、无类定义。
void Foo::Bar() {}

// 命名空间限定的 shared 类型（对应真实 V8 的
// icu::number::LocalizedNumberFormatter）也必须被发现。
void UseIcuFormatter() {
  auto formatter = std::make_shared<icu::number::LocalizedNumberFormatter>();
  (void)formatter;
}

// 注释与字符串中的 new 不得产生 family。
// new GhostFromComment();
const char* kGhost = "new GhostFromString();";

static Observer g_default_observer;

}  // namespace demo
