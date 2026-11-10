#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"

#include "esp_log.h"


#define CONNECTED_BIT BIT0
static EventGroupHandle_t connected_event_group;

#define DELAY 50
#define POLLING_TIME 1500

static const char *TAG = "control";

static void clear_bits(void)
{
    xEventGroupClearBits(
        connected_event_group,
        CONNECTED_BIT
    );
}

static void set_bits(void)
{
    xEventGroupSetBits(
        connected_event_group,
        CONNECTED_BIT
    );
}

static bool bits_is_set(void)
{
    EventBits_t bits = xEventGroupGetBits(connected_event_group);
    return bits & CONNECTED_BIT;
}

void init_event_group(void (* set_callback)(void))
{
    connected_event_group = xEventGroupCreate();

    if (connected_event_group == NULL) {
        ESP_LOGE(
            TAG,
            "Failed to create event group"
        );
    }

    set_bits();
    set_callback();
}

void wait_bits(void)
{
    xEventGroupWaitBits(
        connected_event_group,
        CONNECTED_BIT,
        pdFALSE,       // Не очищать бит после ожидания.
        pdTRUE,        // Ждать все указанные биты.
        portMAX_DELAY  // Ждать бесконечно.
    );
}

void do_control(
    bool (* condition)(void),
    void (* clear_callback)(void),
    void (* set_callback)(void)
)
{
    if (condition()) {
        int cnt = POLLING_TIME;

        while (cnt != 0) {
            if (!condition()) {
                break;
            }
            vTaskDelay(
                pdMS_TO_TICKS(DELAY)
            );
            cnt -= DELAY;
        }

        while (condition()) {}

        if (cnt == 0) {
            ESP_LOGI("BUTTON", "Button held");

            if (bits_is_set()) {
                clear_bits();

                clear_callback();
            } else {
                set_bits();

                set_callback();
            }
        }
    }
}