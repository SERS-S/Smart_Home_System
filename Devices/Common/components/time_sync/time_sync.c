#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "freertos/event_groups.h"

#include "esp_log.h"
#include "esp_sntp.h"

static EventGroupHandle_t event_group;

#define TIME_SYNCED_BIT BIT0

static const char *TAG = "time sync";

static void time_sync_notification_cb(struct timeval *tv)
{
    (void)tv;

    ESP_LOGI(TAG, "Time synchronized");

    xEventGroupSetBits(
        event_group,
        TIME_SYNCED_BIT
    );
}

static void start_time_sync(void)
{
    esp_sntp_setoperatingmode(SNTP_OPMODE_POLL);

    esp_sntp_setservername(
        0,
        "pool.ntp.org"
    );

    esp_sntp_set_time_sync_notification_cb(
        time_sync_notification_cb
    );

    esp_sntp_init();
}


static void wait_for_time_sync(void)
{
    xEventGroupWaitBits(
        event_group,
        TIME_SYNCED_BIT,
        pdFALSE,
        pdTRUE,
        portMAX_DELAY
    );
}

void time_sync(void)
{
    event_group = xEventGroupCreate();

    if (event_group == NULL) {
        ESP_LOGE(TAG, "Failed to create event group");
        return;
    }
    
    start_time_sync();
    wait_for_time_sync();
}