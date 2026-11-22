FROM apache/rocketmq:5.3.4

USER rocketmq
RUN mkdir -p /home/rocketmq/store
